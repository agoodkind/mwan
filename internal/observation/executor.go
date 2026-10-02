// Package observation executes configured health checks and separates missing evidence from target failures.
package observation

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/ops"
	"goodkind.io/mwan/internal/wanstate"
)

// PublicAssignment projects the current provider assignment and its acquisition lifetime.
type PublicAssignment struct {
	Value      netip.Prefix `json:"value"`
	Source     string       `json:"source"`
	AcquiredAt time.Time    `json:"acquired_at"`
	ValidUntil *time.Time   `json:"valid_until,omitempty"`
	Valid      bool         `json:"valid"`
}

// PathIdentity maps a measured route to current configured provider and router identities.
type PathIdentity struct {
	Interface    string             `json:"interface,omitempty"`
	ConnectionID string             `json:"connection_id,omitempty"`
	NextHop      netip.Addr         `json:"next_hop,omitzero"`
	Router       Router             `json:"router,omitempty"`
	ObservedAt   time.Time          `json:"observed_at"`
	Assignments  []PublicAssignment `json:"assignments,omitempty"`
}

// RuntimeConfig supplies execution dependencies and protected credential file references.
type RuntimeConfig struct {
	MachineIDPath            string          `json:"machine_id_path"`
	ProbeBinary              string          `json:"probe_binary"`
	TransportOverheadSeconds int             `json:"transport_overhead_seconds"`
	CloudflareAccountID      string          `json:"cloudflare_account_id,omitempty"`
	CloudflareTokenFile      string          `json:"cloudflare_token_file,omitempty"`
	Paths                    []PathIdentity  `json:"paths,omitempty"`
	GuestOps                 *ops.RealOps    `json:"-"`
	State                    *wanstate.Store `json:"-"`
	Clock                    clock.Clock     `json:"-"`
}

// Executor performs configured checks on their specified observers.
type Executor struct {
	config RuntimeConfig
	log    *slog.Logger
}

// NewExecutor uses the existing guest transport and current operational state.
func NewExecutor(cfg RuntimeConfig, log *slog.Logger) *Executor {
	if log == nil {
		log = slog.Default()
	}
	if cfg.GuestOps == nil {
		var daemonConfig config.Config
		cfg.GuestOps = ops.NewRealOps(&daemonConfig, log)
	}
	if cfg.Clock == nil {
		cfg.Clock = clock.Real{}
	}
	if cfg.TransportOverheadSeconds <= 0 {
		cfg.TransportOverheadSeconds = 5
	}
	return &Executor{config: cfg, log: log}
}

// Validate rejects incomplete probe expectations and observer identities.
func Validate(spec CheckSpec) error {
	if spec.ID == "" || spec.Target == "" || spec.Observer.MachineID == "" {
		return errors.New("check ID, target and observer machine ID are required")
	}
	if spec.TimeoutSeconds <= 0 || spec.MaxAgeSeconds <= 0 {
		return errors.New("positive timeout and maximum age are required")
	}
	if spec.Family != FamilyIPv4 && spec.Family != FamilyIPv6 {
		return errors.New("IPv4 or IPv6 family is required")
	}
	if spec.Source.IsValid() && spec.Source.Is4() != (spec.Family == FamilyIPv4) {
		return errors.New("source address does not match check family")
	}
	switch spec.Observer.Kind {
	case EndpointLocal:
		if spec.Observer.VMID != 0 || spec.Observer.HostMachineID != "" {
			return errors.New("local observer cannot specify a guest or host")
		}
	case EndpointQEMU, EndpointLXC:
		if spec.Observer.VMID <= 0 || spec.Observer.HostMachineID == "" {
			return errors.New("remote observer requires VMID and host machine ID")
		}
	default:
		return errors.New("unsupported observer kind")
	}
	switch spec.Dimension {
	case DimensionDownstreamApplication, DimensionInboundApplication, DimensionConnectionDistribution, DimensionInboundPool, DimensionProviderEgress, DimensionPingPath, DimensionPublicIP:
	default:
		return errors.New("unsupported observation dimension")
	}
	return validateOperation(spec)
}

func validateOperation(spec CheckSpec) error {
	switch spec.Operation {
	case OperationHTTP, OperationPublicIP, OperationDistribution:
		return validateHTTP(spec)
	case OperationDNS:
		server, err := netip.ParseAddrPort(spec.DNSServer)
		if err != nil || server.Addr().Is4() != (spec.Family == FamilyIPv4) || server.Port() == 0 {
			return errors.New("DNS server address and port must match check family")
		}
		if len(spec.DNSExpectedAddresses) == 0 {
			return errors.New("expected DNS addresses are required")
		}
	case OperationPing:
		address, err := netip.ParseAddr(spec.Target)
		if err != nil || address.Is4() != (spec.Family == FamilyIPv4) {
			return errors.New("ping target must match check family")
		}
	case OperationSSHBanner:
		if _, _, err := net.SplitHostPort(spec.Target); err != nil {
			return errors.New("invalid SSH target address and port")
		}
		if !strings.HasPrefix(spec.ExpectedSSHVersion, "SSH-2.0-") {
			return errors.New("expected SSH server version must begin with SSH-2.0-")
		}
	case OperationCloudflarePool:
		if spec.CloudflarePoolID == "" {
			return errors.New("cloudflare pool ID is required")
		}
	default:
		return errors.New("unsupported observation operation")
	}
	return nil
}

func validateHTTP(spec CheckSpec) error {
	if spec.Operation == OperationPublicIP {
		if spec.PublicIPPolicy != PublicIPFamilyOnly && spec.PublicIPPolicy != PublicIPCurrentAssignment {
			return errors.New("public address expectation policy is required")
		}
		if spec.PublicIPPolicy == PublicIPCurrentAssignment && spec.ConnectionID == "" {
			return errors.New("current assignment public address policy requires a connection")
		}
	}
	target, err := url.Parse(spec.Target)
	if err != nil || target.Host == "" || target.User != nil || (target.Scheme != "http" && target.Scheme != "https") {
		return errors.New("HTTP target must be an HTTP or HTTPS URL without credentials")
	}
	if spec.HTTPMethod != "" && spec.HTTPMethod != http.MethodGet && spec.HTTPMethod != http.MethodHead {
		return errors.New("HTTP method must be GET or HEAD")
	}
	if len(spec.ExpectedHTTPStatus) == 0 {
		return errors.New("positive HTTP status expectations are required")
	}
	for _, status := range spec.ExpectedHTTPStatus {
		if status < 100 || status > 599 {
			return errors.New("invalid expected HTTP status")
		}
	}
	return nil
}

// Run executes the configured probe and verifies its observer and required path.
func (executor *Executor) Run(ctx context.Context, spec CheckSpec) Result {
	var result Result
	result.CheckID, result.Dimension, result.Operation = spec.ID, spec.Dimension, spec.Operation
	result.Target, result.PublicIPPolicy, result.Family = spec.Target, spec.PublicIPPolicy, spec.Family
	result.Observer, result.ObservedAt = spec.Observer, executor.config.Clock.Now().UTC()
	result.Availability, result.Outcome = AvailabilityError, OutcomeUnknown
	if err := Validate(spec); err != nil {
		result.Reason = err.Error()
		return result
	}
	if ctx.Err() != nil {
		result.Reason = "observation context has ended"
		return result
	}
	identity, err := os.ReadFile(executor.config.MachineIDPath)
	if err != nil {
		result.Reason = "observer machine identity is unavailable"
		return result
	}
	machineID := strings.TrimSpace(string(identity))
	if spec.Observer.Kind != EndpointLocal {
		return executor.remote(ctx, spec, result, machineID)
	}
	result.Observer.MachineID = machineID
	if machineID != spec.Observer.MachineID {
		result.Reason = "observer machine identity does not match"
		return result
	}
	if spec.Interface != "" {
		if _, err := net.InterfaceByName(spec.Interface); err != nil {
			result.Reason = "configured observer interface is unavailable"
			return result
		}
	}
	probeContext, cancel := context.WithTimeout(ctx, time.Duration(spec.TimeoutSeconds)*time.Second)
	defer cancel()
	switch spec.Operation {
	case OperationHTTP, OperationPublicIP:
		result = executor.http(probeContext, spec, result)
	case OperationDNS:
		result = executor.dns(probeContext, spec, result)
	case OperationPing:
		result = executor.ping(probeContext, spec, result)
	case OperationSSHBanner:
		result = executor.sshBanner(probeContext, spec, result)
	case OperationDistribution, OperationCloudflarePool:
		result.Availability = AvailabilityMissing
		result.Reason = "this operation requires its configured state integration"
	}
	if ctx.Err() != nil {
		result.Availability, result.Outcome, result.Reason = AvailabilityError, OutcomeUnknown, "observation context ended"
		return result
	}
	result = verifyRequiredPath(spec, result)
	result.ObservedAt = executor.config.Clock.Now().UTC()
	return result
}

func verifyRequiredPath(spec CheckSpec, result Result) Result {
	if result.Availability == AvailabilityComplete && result.Outcome == OutcomePass {
		if (spec.Source.IsValid() && !result.Path.Source.IsValid()) ||
			(spec.Interface != "" && result.Path.Interface == "") ||
			(spec.ConnectionID != "" && result.Path.ConnectionID == "") ||
			(spec.Router != "" && result.Path.Router == "") {
			result.Availability, result.Outcome, result.Reason = AvailabilityMissing, OutcomeUnknown, "successful reply lacks the required source or selected path proof"
			return result
		}
		if (spec.Source.IsValid() && spec.Source != result.Path.Source) ||
			(spec.Interface != "" && spec.Interface != result.Path.Interface) ||
			(spec.ExpectedNextHop.IsValid() && spec.ExpectedNextHop != result.Path.NextHop) ||
			(spec.ConnectionID != "" && spec.ConnectionID != result.Path.ConnectionID) ||
			(spec.Router != "" && spec.Router != result.Path.Router) {
			result.Outcome = OutcomeFail
			result.Reason = "reply source or selected path does not match the required path"
		}
	}
	return result
}

func (executor *Executor) remote(ctx context.Context, spec CheckSpec, result Result, machineID string) Result {
	executor.log.InfoContext(ctx, "remote observation", "check_id", spec.ID, "observer_kind", spec.Observer.Kind, "vmid", spec.Observer.VMID)
	if machineID != spec.Observer.HostMachineID || executor.config.ProbeBinary == "" {
		result.Reason = "observer host identity or probe executable is unavailable"
		return result
	}
	localSpec := spec
	localSpec.Observer = Endpoint{Kind: EndpointLocal, MachineID: spec.Observer.MachineID, VMID: 0, HostMachineID: ""}
	encoded, err := json.Marshal(localSpec)
	if err != nil {
		result.Reason = "encode observer request"
		return result
	}
	paths, err := json.Marshal(executor.paths())
	if err != nil || len(paths) > 64*1024 || len(encoded) > 64*1024 {
		result.Reason = "observer metadata exceeds its encoding limit"
		return result
	}
	commandContext, cancel := context.WithTimeout(ctx, (time.Duration(spec.TimeoutSeconds)+time.Duration(executor.config.TransportOverheadSeconds))*time.Second)
	defer cancel()
	var output []byte
	if spec.Observer.Kind == EndpointLXC {
		command := exec.CommandContext(commandContext, "pct")
		command.Args = append(command.Args, "exec", strconv.Itoa(spec.Observer.VMID), "--", executor.config.ProbeBinary, "observe", "--check", string(encoded), "--paths", string(paths))
		output, err = command.Output()
	} else {
		var reply ops.GuestExecResult
		reply, err = executor.config.GuestOps.GuestExec(commandContext, strconv.Itoa(spec.Observer.VMID), executor.config.ProbeBinary, "observe", "--check", string(encoded), "--paths", string(paths))
		output = []byte(reply.Stdout)
		if err == nil && reply.ExitCode != 0 {
			err = fmt.Errorf("observer exited with status %d", reply.ExitCode)
		}
	}
	if err != nil {
		result.Reason = "remote observer execution failed"
		return result
	}
	var remote Result
	if err := json.Unmarshal(output, &remote); err != nil {
		result.Reason = "remote observer returned invalid result JSON"
		return result
	}
	if remote.CheckID != spec.ID || remote.Observer != localSpec.Observer || remote.Family != spec.Family ||
		remote.Operation != spec.Operation || remote.Dimension != spec.Dimension {
		result.Reason = "remote result does not match the observer request"
		return result
	}
	remote.Observer = spec.Observer
	age := executor.config.Clock.Now().Sub(remote.ObservedAt)
	if remote.ObservedAt.IsZero() || age < 0 || age > time.Duration(spec.MaxAgeSeconds)*time.Second {
		remote.Availability, remote.Outcome, remote.Reason = AvailabilityStale, OutcomeUnknown, "remote observation timestamp is outside its required lifetime"
	}
	return remote
}

func family(spec CheckSpec) string {
	if spec.Family == FamilyIPv4 {
		return "inet"
	}
	return "inet6"
}

func (executor *Executor) http(ctx context.Context, spec CheckSpec, result Result) Result {
	method := spec.HTTPMethod
	if method == "" {
		method = http.MethodGet
	}
	probe, err := netif.HTTPProbe(ctx, netif.HTTPProbeSpec{
		Interface: spec.Interface, Family: family(spec), Source: spec.Source,
		URL: spec.Target, Method: method, Timeout: time.Duration(spec.TimeoutSeconds) * time.Second,
	})
	result.Path.Source, result.Path.Destination = probe.Source, probe.Destination
	result.HTTPStatus, result.ResponseBody = probe.StatusCode, probe.Body
	if err != nil {
		return failedProbe(result, err)
	}
	result.Availability, result.Outcome = AvailabilityComplete, OutcomeFail
	if !slices.Contains(spec.ExpectedHTTPStatus, probe.StatusCode) {
		result.Reason = "HTTP status does not match the expected response"
		return result
	}
	if spec.ExpectedBody != "" && probe.Body != spec.ExpectedBody {
		result.Reason = "HTTP body does not match the expected response"
		return result
	}
	if spec.Operation == OperationPublicIP {
		result = executor.checkPublicIP(spec, result, probe.Body)
		if result.Outcome != OutcomePass {
			return result
		}
	}
	executor.route(ctx, spec, &result)
	if result.Availability != AvailabilityComplete {
		return result
	}
	result.Outcome, result.Reason = OutcomePass, "expected HTTP response received"
	return result
}

func (executor *Executor) checkPublicIP(spec CheckSpec, result Result, body string) Result {
	address, err := netip.ParseAddr(body)
	result.PublicIP = address.Unmap()
	if err != nil || result.PublicIP.Is4() != (spec.Family == FamilyIPv4) {
		result.Reason = "public address response does not match the requested family"
		return result
	}
	if spec.PublicIPPolicy == PublicIPCurrentAssignment {
		valid, available := executor.publicAddressValid(spec, result.PublicIP)
		if !available {
			result.Availability, result.Outcome, result.Reason = AvailabilityMissing, OutcomeUnknown, "current connection assignments are unavailable"
			return result
		}
		if !valid {
			result.Reason = "public address is outside the current valid connection assignments"
			return result
		}
	}
	result.Outcome = OutcomePass
	return result
}

func (executor *Executor) dns(ctx context.Context, spec CheckSpec, result Result) Result {
	probe, err := netif.DNSProbe(ctx, netif.DNSProbeSpec{
		Interface: spec.Interface, Family: family(spec), Source: spec.Source,
		Server: spec.DNSServer, Name: spec.Target, Timeout: time.Duration(spec.TimeoutSeconds) * time.Second,
	})
	result.DNSAddresses, result.Path.Source, result.Path.Destination = probe.Addresses, probe.Source, probe.Server
	if err != nil {
		return failedProbe(result, err)
	}
	if !probe.Source.IsValid() || !probe.Server.IsValid() {
		result.Availability, result.Outcome, result.Reason = AvailabilityMissing, OutcomeUnknown, "configured DNS resolver query was not observed"
		return result
	}
	result.Availability, result.Outcome = AvailabilityComplete, OutcomeFail
	for _, address := range spec.DNSExpectedAddresses {
		if !slices.Contains(probe.Addresses, address) {
			result.Reason = "DNS response omits an expected address"
			return result
		}
	}
	executor.route(ctx, spec, &result)
	if result.Availability != AvailabilityComplete {
		return result
	}
	result.Outcome, result.Reason = OutcomePass, "expected DNS addresses received"
	return result
}

func (executor *Executor) ping(ctx context.Context, spec CheckSpec, result Result) Result {
	address, err := netip.ParseAddr(spec.Target)
	if err != nil {
		result.Reason = "invalid ping target"
		return result
	}
	result.Path.Destination = address
	probe, err := netif.PingProbe(ctx, spec.Interface, spec.Source, address, time.Duration(spec.TimeoutSeconds)*time.Second)
	result.Path.Source = probe.Source
	if err != nil {
		return failedProbe(result, err)
	}
	result.Availability, result.Outcome, result.Reason = AvailabilityComplete, OutcomePass, "ICMP reply received"
	executor.route(ctx, spec, &result)
	return result
}

func (executor *Executor) sshBanner(ctx context.Context, spec CheckSpec, result Result) Result {
	connection, err := netif.DialProbe(ctx, spec.Interface, spec.Source, family(spec), spec.Target, time.Duration(spec.TimeoutSeconds)*time.Second)
	if err != nil {
		return failedProbe(result, err)
	}
	defer func() { _ = connection.Close() }()
	if address, ok := connection.LocalAddr().(*net.TCPAddr); ok {
		result.Path.Source = address.AddrPort().Addr().Unmap()
	}
	if address, ok := connection.RemoteAddr().(*net.TCPAddr); ok {
		result.Path.Destination = address.AddrPort().Addr().Unmap()
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		result.Reason = "SSH banner deadline is unavailable"
		return result
	}
	if err := connection.SetReadDeadline(deadline); err != nil {
		result.Reason = "SSH banner deadline failed"
		return result
	}
	reader := bufio.NewReaderSize(connection, 256)
	for range 32 {
		line, readErr := reader.ReadSlice('\n')
		if readErr != nil {
			return failedProbe(result, readErr)
		}
		version := strings.TrimSpace(string(line))
		if !strings.HasPrefix(version, "SSH-") {
			continue
		}
		result.SSHVersion = version
		result.Availability, result.Outcome = AvailabilityComplete, OutcomeFail
		if version != spec.ExpectedSSHVersion {
			result.Reason = "SSH server version does not match"
			return result
		}
		executor.route(ctx, spec, &result)
		if result.Availability != AvailabilityComplete {
			return result
		}
		result.Outcome, result.Reason = OutcomePass, "expected SSH server version received"
		return result
	}
	result.Availability, result.Outcome, result.Reason = AvailabilityComplete, OutcomeFail, "SSH response contains no server version"
	return result
}

func failedProbe(result Result, err error) Result {
	if errors.Is(err, context.Canceled) {
		result.Reason = "observation canceled"
		return result
	}
	if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.EADDRNOTAVAIL) || errors.Is(err, unix.ENODEV) {
		result.Reason = "observer lacks the required socket permission or source device"
		return result
	}
	var networkError net.Error
	var operationError *net.OpError
	if errors.As(err, &operationError) && (operationError.Op == "control" || operationError.Op == "listen") {
		result.Reason = "observer socket setup failed"
		return result
	}
	result.Availability, result.Outcome = AvailabilityComplete, OutcomeFail
	result.Reason = "target probe failed"
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &networkError) && networkError.Timeout()) {
		result.Reason = "target reply timed out"
	}
	return result
}

func (executor *Executor) route(ctx context.Context, spec CheckSpec, result *Result) {
	if !result.Path.Destination.IsValid() {
		return
	}
	source := ""
	if result.Path.Source.IsValid() {
		source = result.Path.Source.String()
	}
	route, found, err := netif.RouteLookup(ctx, executor.log, family(spec), result.Path.Destination.String(), source, 0)
	if err != nil {
		result.Availability, result.Outcome, result.Reason = AvailabilityError, OutcomeUnknown, "selected path query failed"
		return
	}
	if !found {
		result.Availability, result.Outcome, result.Reason = AvailabilityMissing, OutcomeUnknown, "selected path is unavailable"
		return
	}
	result.Path.Interface = route.OIF
	if route.Gateway != "" {
		result.Path.NextHop, _ = netip.ParseAddr(route.Gateway)
	}
	for _, identity := range executor.paths() {
		age := executor.config.Clock.Now().Sub(identity.ObservedAt)
		if identity.ObservedAt.IsZero() || age < 0 || age > time.Duration(spec.MaxAgeSeconds)*time.Second {
			continue
		}
		if identity.Interface != "" && identity.Interface == route.OIF {
			result.Path.ConnectionID = identity.ConnectionID
		}
		if identity.NextHop.IsValid() && identity.NextHop == result.Path.NextHop {
			result.Path.Router = identity.Router
		}
	}
}

func (executor *Executor) paths() []PathIdentity {
	paths := slices.Clone(executor.config.Paths)
	if executor.config.State == nil {
		return paths
	}
	for _, connection := range executor.config.State.Snapshot().Connections {
		var assignments []PublicAssignment
		for _, assignment := range slices.Concat(connection.IPv4.Assignments, connection.IPv6.Assignments) {
			assignments = append(assignments, PublicAssignment{
				Value: assignment.Value, Source: assignment.Source,
				AcquiredAt: assignment.AcquiredAt, ValidUntil: assignment.ValidUntil, Valid: assignment.Valid,
			})
		}
		paths = append(paths, PathIdentity{
			Interface: connection.ActualName, ConnectionID: connection.ID,
			ObservedAt: connection.ObservedAt, Assignments: assignments, NextHop: netip.Addr{}, Router: "",
		})
	}
	return paths
}

func (executor *Executor) publicAddressValid(spec CheckSpec, address netip.Addr) (bool, bool) {
	now := executor.config.Clock.Now()
	available := false
	for _, identity := range executor.paths() {
		age := now.Sub(identity.ObservedAt)
		if identity.ConnectionID != spec.ConnectionID || identity.ObservedAt.IsZero() || age < 0 || age > time.Duration(spec.MaxAgeSeconds)*time.Second {
			continue
		}
		for _, assignment := range identity.Assignments {
			if !assignment.Valid || !assignment.Value.IsValid() || (assignment.ValidUntil != nil && !now.Before(*assignment.ValidUntil)) {
				continue
			}
			available = true
			if assignment.Value.Contains(address) {
				return true, true
			}
		}
	}
	return false, available
}
