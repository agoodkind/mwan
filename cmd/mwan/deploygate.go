package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/wanroutes"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
)

const (
	deployGateEgressTargetV6   = "2606:4700:4700::1111"
	deployGateEgressTargetV4   = "1.1.1.1"
	deployGateProbeTimeout     = 2 * time.Second
	deployGatePollInterval     = 1 * time.Second
	deployGateRebootPoll       = 3 * time.Second
	deployGateGuestExecTimeout = 5 * time.Second
	// deployGateOwnedBudget bounds the wait for the routing module to hold
	// every on-link mapped address once egress has returned. The module holds
	// them on its first reconcile, so a minute covers a slow daemon start.
	deployGateOwnedBudget = 60 * time.Second
	// deployGateGuestBinary is where the deploy installs the mwan binary inside
	// the gateway guest.
	deployGateGuestBinary = "/usr/local/bin/mwan"
	// deployGateHostPrefixBits is the prefix length of a single IPv4 address.
	deployGateHostPrefixBits = 32
	// deployGateAlertKind names the owned-address alert in the notify state.
	deployGateAlertKind = "deploy_gate_owned_address_missing"
	// deployGateAlertTimeout bounds the alert send, so a wedged mail transport
	// cannot hold the transient gate unit open.
	deployGateAlertTimeout = 30 * time.Second
	// deployGateAlertService identifies the gate on the outgoing alert email.
	deployGateAlertService = "mwan-deploy-gate"

	exitDeployGateOK           = 0
	exitDeployGateFailed       = 1
	exitDeployGateUnobservable = 2
	exitDeployGateUsage        = 64
)

// deployGateMode is the typed enum of deploy-gate verbs.
type deployGateMode string

const (
	gateModeCheckEgress deployGateMode = "check-egress"
	gateModeWaitReboot  deployGateMode = "wait-reboot"
	gateModeWaitEgress  deployGateMode = "wait-egress"
	gateModeWaitDeploy  deployGateMode = "wait-deploy"
	gateModeCheckOwned  deployGateMode = "check-owned-addresses"
)

// traceIDPattern bounds the trace id because it lands in the verdict file
// and in a systemd unit name; anything outside this shape is operator error.
var traceIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// gateNotRun marks a verdict slot whose gate deliberately did not run: the
// egress slot after a definitive reboot failure, and the owned-address slot
// whenever egress did not pass.
const gateNotRun = -1

// gateVerdict is the JSON contract between wait-deploy on the hypervisor
// and the deploy play's collector on the controller. TraceID and OldBootID
// key the verdict to one deploy run, so a stale file from an earlier run is
// never accepted as this run's result.
type gateVerdict struct {
	TraceID    string `json:"trace_id"`
	OldBootID  string `json:"old_boot_id"`
	RebootRC   int    `json:"reboot_rc"`
	EgressRC   int    `json:"egress_rc"`
	OwnedRC    int    `json:"owned_rc"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
}

// bootIDPattern matches the kernel's boot_id UUID exactly, so a truncated or
// error-shaped guest-agent response never passes as a boot id.
var bootIDPattern = regexp.MustCompile(
	`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

// deployGateDeps carries the gate's side-effecting operations so the verdict
// logic is testable without a hypervisor. The real wiring lives in
// newDeployGateDeps; tests substitute canned readers and probes.
type deployGateDeps struct {
	out        io.Writer
	ping6      func(context.Context, netip.Addr, time.Duration) (time.Duration, error)
	ping4      func(context.Context, string, netip.Addr, time.Duration) (time.Duration, error)
	readBootID func(ctx context.Context, vmid int) (string, error)
	now        func() time.Time
	sleep      func(d time.Duration)
	// loadNetwork and listAddrs serve the owned-address check inside the guest.
	loadNetwork func() (*networkjson.Config, error)
	listAddrs   func(ctx context.Context, log *slog.Logger, iface string) ([]netif.CurrentAddr, error)
	// runGuestOwnedCheck runs that check from the hypervisor through the guest
	// agent.
	runGuestOwnedCheck func(ctx context.Context, vmid int) (guestExecResponse, error)
	// alertOwnedMissing emails a failed owned-address verdict.
	alertOwnedMissing func(ctx context.Context, alert ownedMissingAlert) error
}

// ownedMissingAlert is what the owned-address alert email carries: the guest,
// the deploy run, and the guest's last owned-address report.
type ownedMissingAlert struct {
	VMID    int
	TraceID string
	Report  string
}

func newDeployGateDeps() deployGateDeps {
	probe := netif.NewV6Probe("", nil)
	return deployGateDeps{
		out:        os.Stdout,
		ping6:      probe.PingICMP6,
		ping4:      netif.Ping4,
		readBootID: readGuestBootID,
		now:        time.Now,
		sleep:      time.Sleep,
		loadNetwork: func() (*networkjson.Config, error) {
			return networkjson.Load(networkjson.DefaultPath, networkjson.DefaultSchemaDir)
		},
		listAddrs:          netif.ListAddrs,
		runGuestOwnedCheck: readGuestOwnedCheck,
		alertOwnedMissing:  sendOwnedMissingAlert,
	}
}

// The egress targets are parsed once; probe errors stay local to the gate
// verdicts, which report them in the deploy log instead of returning them.
var (
	deployGateTargetV6 = netip.MustParseAddr(deployGateEgressTargetV6)
	deployGateTargetV4 = netip.MustParseAddr(deployGateEgressTargetV4)
)

// runDeployGate dispatches the deploy-gate modes, the deploy-time
// reachability and reboot gates deploy-mwan.yml runs on the Proxmox host
// that owns the MWAN guest. The host is the only vantage point that stays up
// while the guest reboots, so the playbook pushes this binary there and
// delegates the gates to it. check-owned-addresses is the one mode that runs
// inside the guest, where wait-deploy starts it through the guest agent,
// because only the guest can read its own link addresses.
//
// Exit codes: 0 on success, 1 on a definitive failure, 64 on a usage error,
// and, for wait-reboot only, 2 when the guest is unobservable at the
// deadline. Exit 2 means the verdict belongs to the egress gate: a guest
// that went down and never came back must reach the rollback chain, not a
// fail-without-rollback stop.
//
// args excludes the subcommand name itself.
func runDeployGate(args []string) int {
	deps := newDeployGateDeps()
	ctx := context.Background()
	if len(args) < 1 {
		printDeployGateUsage()
		return exitDeployGateUsage
	}
	rest := args[1:]
	switch deployGateMode(args[0]) {
	case gateModeCheckEgress:
		if len(rest) != 0 {
			printDeployGateUsage()
			return exitDeployGateUsage
		}
		return checkEgress(ctx, deps)
	case gateModeCheckOwned:
		if len(rest) != 0 {
			printDeployGateUsage()
			return exitDeployGateUsage
		}
		return checkOwnedAddresses(ctx, deps)
	case gateModeWaitReboot:
		if len(rest) != 3 {
			printDeployGateUsage()
			return exitDeployGateUsage
		}
		vmid, err := parseVMID(rest[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "mwan deploy-gate: %v\n", err)
			return exitDeployGateUsage
		}
		budget, err := parseBudgetSeconds(rest[2])
		if err != nil {
			fmt.Fprintf(os.Stderr, "mwan deploy-gate: %v\n", err)
			return exitDeployGateUsage
		}
		if !bootIDPattern.MatchString(rest[1]) {
			fmt.Fprintf(os.Stderr,
				"mwan deploy-gate: old_boot_id %q is not a boot_id UUID\n", rest[1])
			return exitDeployGateUsage
		}
		return waitReboot(ctx, deps, vmid, rest[1], budget)
	case gateModeWaitEgress:
		if len(rest) != 1 {
			printDeployGateUsage()
			return exitDeployGateUsage
		}
		budget, err := parseBudgetSeconds(rest[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "mwan deploy-gate: %v\n", err)
			return exitDeployGateUsage
		}
		return waitEgress(ctx, deps, budget)
	case gateModeWaitDeploy:
		inputs, ok := parseWaitDeployArgs(rest)
		if !ok {
			return exitDeployGateUsage
		}
		return waitDeploy(ctx, deps, inputs)
	default:
		printDeployGateUsage()
		return exitDeployGateUsage
	}
}

// waitDeployInputs carries the wait-deploy arguments as one value, because
// six positional parameters invite transposition bugs at the call site.
type waitDeployInputs struct {
	vmid         int
	oldBootID    string
	rebootBudget time.Duration
	egressBudget time.Duration
	traceID      string
	verdictPath  string
}

func parseWaitDeployArgs(rest []string) (waitDeployInputs, bool) {
	if len(rest) != 6 {
		printDeployGateUsage()
		return waitDeployInputs{}, false
	}
	vmid, err := parseVMID(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan deploy-gate: %v\n", err)
		return waitDeployInputs{}, false
	}
	if !bootIDPattern.MatchString(rest[1]) {
		fmt.Fprintf(os.Stderr,
			"mwan deploy-gate: old_boot_id %q is not a boot_id UUID\n", rest[1])
		return waitDeployInputs{}, false
	}
	rebootBudget, err := parseBudgetSeconds(rest[2])
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan deploy-gate: %v\n", err)
		return waitDeployInputs{}, false
	}
	egressBudget, err := parseBudgetSeconds(rest[3])
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan deploy-gate: %v\n", err)
		return waitDeployInputs{}, false
	}
	if !traceIDPattern.MatchString(rest[4]) {
		fmt.Fprintf(os.Stderr,
			"mwan deploy-gate: trace_id %q must match %s\n",
			rest[4], traceIDPattern.String())
		return waitDeployInputs{}, false
	}
	return waitDeployInputs{
		vmid:         vmid,
		oldBootID:    rest[1],
		rebootBudget: rebootBudget,
		egressBudget: egressBudget,
		traceID:      rest[4],
		verdictPath:  rest[5],
	}, true
}

// waitDeploy chains the reboot, egress, and owned-address verdicts and records
// all three in one atomically written verdict file. The process exit code reports only
// whether the verdict was recorded: the verdicts themselves travel in the
// file, so the collector on the controller reads outcomes from there and a
// failed transient unit always means the gate itself died.
//
// The egress gate deliberately does not run when the reboot verdict is
// definitive failure, because the play then fails without rollback and an
// egress verdict would have nothing to decide; that slot records the
// not-run marker instead. The owned-address check runs only after egress
// passed, because it reaches the guest through its agent and asserts state
// the routing module writes once the gateway is up. A failed owned-address
// verdict also sends one alert email, after the verdict file is written, so a
// slow mail transport never delays the collector.
func waitDeploy(ctx context.Context, deps deployGateDeps, in waitDeployInputs) int {
	log := slog.With("component", "deploy-gate", "op", "waitDeploy")
	verdict := gateVerdict{
		TraceID:   in.traceID,
		OldBootID: in.oldBootID,
		StartedAt: deps.now().UTC().Format(time.RFC3339),
		EgressRC:  gateNotRun,
		OwnedRC:   gateNotRun,
	}
	verdict.RebootRC = waitReboot(ctx, deps, in.vmid, in.oldBootID, in.rebootBudget)
	if verdict.RebootRC != exitDeployGateFailed {
		verdict.EgressRC = waitEgress(ctx, deps, in.egressBudget)
	}
	ownedReport := ""
	if verdict.EgressRC == exitDeployGateOK {
		verdict.OwnedRC, ownedReport = waitOwnedAddresses(ctx, deps, in.vmid, deployGateOwnedBudget)
	}
	verdict.FinishedAt = deps.now().UTC().Format(time.RFC3339)
	writeErr := writeVerdictFile(in.verdictPath, verdict)
	if verdict.OwnedRC == exitDeployGateFailed {
		alertErr := deps.alertOwnedMissing(ctx, ownedMissingAlert{
			VMID:    in.vmid,
			TraceID: in.traceID,
			Report:  ownedReport,
		})
		if alertErr != nil {
			fmt.Fprintf(deps.out, "owned-address alert not sent: %v\n", alertErr)
		} else {
			fmt.Fprintln(deps.out, "owned-address alert sent")
		}
	}
	if writeErr != nil {
		fmt.Fprintf(os.Stderr, "mwan deploy-gate: write verdict: %v\n", writeErr)
		return exitDeployGateFailed
	}
	log.InfoContext(ctx, "deploy-gate: verdict recorded",
		"trace_id", verdict.TraceID,
		"reboot_rc", verdict.RebootRC,
		"egress_rc", verdict.EgressRC,
		"owned_rc", verdict.OwnedRC,
		"path", in.verdictPath)
	fmt.Fprintf(deps.out, "verdict recorded: reboot_rc=%d egress_rc=%d owned_rc=%d path=%s\n",
		verdict.RebootRC, verdict.EgressRC, verdict.OwnedRC, in.verdictPath)
	return exitDeployGateOK
}

// writeVerdictFile writes the verdict via a temp file in the target
// directory plus rename, so the collector never reads a partial file: it
// sees either no verdict or a complete one.
func writeVerdictFile(path string, verdict gateVerdict) error {
	log := slog.With("component", "deploy-gate", "op", "writeVerdictFile", "path", path)
	payload, err := json.Marshal(verdict)
	if err != nil {
		log.Warn("deploy-gate: marshal verdict failed", "err", err)
		return fmt.Errorf("marshal verdict: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("deploy-gate: create verdict dir failed", "err", err)
		return fmt.Errorf("create verdict dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".verdict-*")
	if err != nil {
		log.Warn("deploy-gate: create verdict temp file failed", "err", err)
		return fmt.Errorf("create verdict temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		log.Warn("deploy-gate: write verdict temp file failed", "err", err)
		return fmt.Errorf("write verdict temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		log.Warn("deploy-gate: close verdict temp file failed", "err", err)
		return fmt.Errorf("close verdict temp file: %w", err)
	}
	// The collector reads over SSH as a non-root probe in some paths, so the
	// verdict is world-readable; it carries no secrets.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		log.Warn("deploy-gate: chmod verdict temp file failed", "err", err)
		return fmt.Errorf("chmod verdict temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		log.Warn("deploy-gate: rename verdict into place failed", "err", err)
		return fmt.Errorf("rename verdict into place: %w", err)
	}
	return nil
}

// parseVMID accepts only a plain positive integer, which both rules out
// command-injection shapes before the vmid reaches qm and rejects obviously
// wrong operator input.
func parseVMID(raw string) (int, error) {
	vmid, err := strconv.Atoi(raw)
	if err != nil || vmid <= 0 {
		return 0, fmt.Errorf("vmid %q is not a positive integer", raw)
	}
	return vmid, nil
}

func printDeployGateUsage() {
	fmt.Fprintln(os.Stderr,
		"usage: mwan deploy-gate check-egress"+
			" | check-owned-addresses"+
			" | wait-reboot <vmid> <old_boot_id> <seconds>"+
			" | wait-egress <seconds>"+
			" | wait-deploy <vmid> <old_boot_id> <reboot_seconds>"+
			" <egress_seconds> <trace_id> <verdict_path>")
}

func parseBudgetSeconds(raw string) (time.Duration, error) {
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("budget %q is not a positive integer of seconds", raw)
	}
	return time.Duration(seconds) * time.Second, nil
}

// checkEgress probes both families once. Either family answering is enough,
// because this mode only gates whether a pre-deploy snapshot is worth taking.
func checkEgress(ctx context.Context, deps deployGateDeps) int {
	_, err6 := deps.ping6(ctx, deployGateTargetV6, deployGateProbeTimeout)
	_, err4 := deps.ping4(ctx, "", deployGateTargetV4, deployGateProbeTimeout)
	fmt.Fprintf(deps.out, "egress probe: ipv6=%s ipv4=%s\n",
		yesNo(err6 == nil), yesNo(err4 == nil))
	if err6 == nil || err4 == nil {
		return exitDeployGateOK
	}
	fmt.Fprintf(deps.out, "no egress on either family; last v4 error: %v\n", err4)
	return exitDeployGateFailed
}

// checkOwnedAddresses runs inside the gateway. For each provider with static
// mappings it reads the link's addresses and asserts that every mapped address
// the link must hold, by the rule the routing module applies each reconcile, is
// held on the link as a host address. The rule is computed from the link's
// connected subnets rather than from what it holds, so a missing address is
// reported rather than silently left out.
func checkOwnedAddresses(ctx context.Context, deps deployGateDeps) int {
	log := slog.With("component", "deploy-gate", "op", "checkOwnedAddresses")
	network, err := deps.loadNetwork()
	if err != nil {
		fmt.Fprintf(deps.out, "network configuration unreadable: %v\n", err)
		return exitDeployGateFailed
	}
	present := 0
	missing := 0
	for _, name := range slices.Sorted(maps.Keys(network.WAN)) {
		entry := network.WAN[name]
		if len(entry.StaticMappings) == 0 {
			continue
		}
		held, err := deps.listAddrs(ctx, log, entry.Iface)
		if err != nil {
			fmt.Fprintf(deps.out, "addresses on %s unreadable: %v\n", entry.Iface, err)
			return exitDeployGateFailed
		}
		externals := make([]netip.Addr, 0, len(entry.StaticMappings))
		for _, mapping := range entry.StaticMappings {
			externals = append(externals, mapping.External)
		}
		owned, err := wanroutes.OnLinkMappedAddresses(held, externals)
		if err != nil {
			fmt.Fprintf(deps.out, "addresses on %s unreadable: %v\n", entry.Iface, err)
			return exitDeployGateFailed
		}
		for _, address := range owned {
			state := "missing"
			if holdsHostAddress(held, address) {
				state = "present"
				present++
			} else {
				missing++
			}
			fmt.Fprintf(deps.out, "owned address %s on %s: %s\n", address, entry.Iface, state)
		}
	}
	fmt.Fprintf(deps.out, "owned addresses: %d present, %d missing\n", present, missing)
	if missing > 0 {
		return exitDeployGateFailed
	}
	return exitDeployGateOK
}

// holdsHostAddress reports whether held carries address as a /32.
func holdsHostAddress(held []netif.CurrentAddr, address netip.Addr) bool {
	want := netip.PrefixFrom(address, deployGateHostPrefixBits).String()
	for _, current := range held {
		if current.CIDR == want {
			return true
		}
	}
	return false
}

// waitOwnedAddresses polls the guest's owned-address check until it passes or
// the budget runs out, and returns the verdict with the guest's last report. A
// failing check and an unreachable agent both poll again, because the routing
// module may not have reconciled yet; the last report reaches the deploy log
// either way.
func waitOwnedAddresses(
	ctx context.Context, deps deployGateDeps, vmid int, budget time.Duration,
) (int, string) {
	deadline := deps.now().Add(budget)
	lastReport := "no owned-address check completed"
	for deps.now().Before(deadline) {
		response, err := deps.runGuestOwnedCheck(ctx, vmid)
		switch {
		case err != nil:
			lastReport = err.Error()
		case response.ExitCode == exitDeployGateOK:
			report := strings.TrimSpace(response.OutData)
			fmt.Fprintf(deps.out, "owned addresses held: %s\n", report)
			return exitDeployGateOK, report
		default:
			lastReport = response.OutData
		}
		deps.sleep(deployGatePollInterval)
	}
	lastReport = strings.TrimSpace(lastReport)
	fmt.Fprintf(deps.out, "owned addresses not held within %s; last report: %s\n", budget, lastReport)
	return exitDeployGateFailed, lastReport
}

// sendOwnedMissingAlert emails a failed owned-address verdict through the
// notify package, with the email settings the hypervisor's own daemons load
// from its configuration. The gate runs on that hypervisor, so it reuses the
// alert path the watchdog already sends through rather than carrying mail
// settings of its own.
func sendOwnedMissingAlert(ctx context.Context, alert ownedMissingAlert) error {
	log := slog.With("component", "deploy-gate", "op", "sendOwnedMissingAlert",
		"vmid", alert.VMID, "trace_id", alert.TraceID)
	cfg, err := config.Load()
	if err != nil {
		log.ErrorContext(ctx, "deploy-gate: configuration unreadable; alert not sent", "err", err)
		return fmt.Errorf("load configuration: %w", err)
	}
	// notify.FromConfig degrades to a journal-only notifier when email is
	// unconfigured, which would drop the alert silently, so that case is named.
	if cfg.Email.SMTP2GOAPIKey == "" || cfg.Email.AlertEmail == "" {
		unconfigured := errors.New("email unconfigured in the hypervisor configuration")
		log.ErrorContext(ctx, "deploy-gate: alert not sent", "err", unconfigured)
		return unconfigured
	}
	sendCtx, cancel := context.WithTimeout(ctx, deployGateAlertTimeout)
	defer cancel()
	notifier := notify.FromConfig(cfg, log, deployGateAlertService)
	notifier.Notify(sendCtx, notify.Event{
		Now:     time.Now(),
		Level:   slog.LevelError,
		Kind:    deployGateAlertKind,
		Key:     alert.TraceID,
		Message: fmt.Sprintf("deploy gate: VM %d does not hold its on-link mapped addresses", alert.VMID),
		Fields: []slog.Attr{
			slog.Int("vmid", alert.VMID),
			slog.String("trace_id", alert.TraceID),
			slog.String("report", alert.Report),
		},
		IsRecovery: false,
	})
	return nil
}

// readGuestOwnedCheck runs the owned-address check inside the guest through
// the QEMU guest agent and returns its exit code and report.
func readGuestOwnedCheck(ctx context.Context, vmid int) (guestExecResponse, error) {
	log := slog.With("component", "deploy-gate", "op", "readGuestOwnedCheck", "vmid", vmid)
	raw, err := ops.GuestExecViaQm(ctx,
		deployGateGuestExecTimeout+5*time.Second, deployGateGuestExecTimeout,
		vmid, deployGateGuestBinary, "deploy-gate", string(gateModeCheckOwned))
	if err != nil {
		log.WarnContext(ctx, "deploy-gate: qm guest exec failed", "err", err)
		return guestExecResponse{ExitCode: 0, OutData: ""}, fmt.Errorf("qm guest exec %d: %w", vmid, err)
	}
	var response guestExecResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		log.WarnContext(ctx, "deploy-gate: qm guest exec response is not JSON", "err", err)
		return guestExecResponse{ExitCode: 0, OutData: ""}, fmt.Errorf("parse qm guest exec response: %w", err)
	}
	return response, nil
}

// waitReboot polls the guest's boot_id until it differs from oldBootID. At the
// deadline one final read decides the verdict: a guest still answering with
// the old boot_id definitively never rebooted (exit 1, the playbook fails
// without rollback because the VM still runs the deployed config), while an
// unreachable guest is deferred to the egress gate (exit 2), whose failure
// path owns rollback for a guest that went down and never came back.
func waitReboot(
	ctx context.Context,
	deps deployGateDeps,
	vmid int,
	oldBootID string,
	budget time.Duration,
) int {
	deadline := deps.now().Add(budget)
	var lastReadErr error
	for deps.now().Before(deadline) {
		current, err := deps.readBootID(ctx, vmid)
		if err != nil {
			// The guest agent is unreachable through shutdown and early
			// boot, so a failed read is a normal poll outcome while the
			// reboot is in flight.
			lastReadErr = err
		} else if current != oldBootID {
			fmt.Fprintf(deps.out, "guest %d rebooted: boot_id %s changed to %s\n",
				vmid, oldBootID, current)
			return exitDeployGateOK
		}
		deps.sleep(deployGateRebootPoll)
	}
	final, err := deps.readBootID(ctx, vmid)
	if err == nil && final == oldBootID {
		fmt.Fprintf(deps.out,
			"guest %d still runs boot_id %s after %s, so the reboot never fired\n",
			vmid, oldBootID, budget)
		return exitDeployGateFailed
	}
	if err == nil {
		fmt.Fprintf(deps.out, "guest %d rebooted: boot_id %s changed to %s\n",
			vmid, oldBootID, final)
		return exitDeployGateOK
	}
	fmt.Fprintf(deps.out,
		"guest %d was unobservable at the %s deadline (last agent error: %v);"+
			" deferring the reboot verdict to the egress gate\n",
		vmid, budget, errorOrPrior(err, lastReadErr))
	return exitDeployGateUnobservable
}

// errorOrPrior prefers the final read error but falls back to the last error
// seen during polling, so the log names a cause even when the final error is
// generic.
func errorOrPrior(final error, prior error) error {
	if final != nil {
		return final
	}
	return prior
}

// waitEgress polls until IPv6 egress returns. IPv6 decides the verdict
// because IPv6 is the primary family here; the IPv4 result rides along in
// the success line for the deploy log.
func waitEgress(ctx context.Context, deps deployGateDeps, budget time.Duration) int {
	deadline := deps.now().Add(budget)
	var lastErr error
	for deps.now().Before(deadline) {
		_, err6 := deps.ping6(ctx, deployGateTargetV6, deployGateProbeTimeout)
		_, err4 := deps.ping4(ctx, "", deployGateTargetV4, deployGateProbeTimeout)
		if err6 == nil {
			fmt.Fprintf(deps.out, "egress restored: ipv6=yes ipv4=%s\n",
				yesNo(err4 == nil))
			return exitDeployGateOK
		}
		lastErr = err6
		deps.sleep(deployGatePollInterval)
	}
	fmt.Fprintf(deps.out, "no IPv6 egress within %s; last probe error: %v\n",
		budget, lastErr)
	return exitDeployGateFailed
}

func yesNo(ok bool) string {
	if ok {
		return "yes"
	}
	return "no"
}

// guestExecResponse is the JSON shape `qm guest exec` prints on success.
type guestExecResponse struct {
	ExitCode int    `json:"exitcode"`
	OutData  string `json:"out-data"`
}

// readGuestBootID reads /proc/sys/kernel/random/boot_id inside the guest
// through the QEMU guest agent and validates the response shape, so a
// partial or error-shaped reply never compares equal or unequal by accident.
func readGuestBootID(ctx context.Context, vmid int) (string, error) {
	log := slog.With("component", "deploy-gate", "op", "readGuestBootID", "vmid", vmid)
	log.DebugContext(ctx, "deploy-gate: reading guest boot_id")
	raw, err := ops.GuestExecViaQm(ctx,
		deployGateGuestExecTimeout+5*time.Second, deployGateGuestExecTimeout,
		vmid, "cat", "/proc/sys/kernel/random/boot_id")
	if err != nil {
		log.WarnContext(ctx, "deploy-gate: qm guest exec failed", "err", err)
		return "", fmt.Errorf("qm guest exec %d: %w", vmid, err)
	}
	bootID, err := unmarshalGuestBootID(raw)
	if err != nil {
		log.WarnContext(ctx, "deploy-gate: guest boot_id response invalid", "err", err)
		return "", err
	}
	return bootID, nil
}

// unmarshalGuestBootID extracts and validates the boot_id from a qm guest
// exec JSON response.
func unmarshalGuestBootID(raw []byte) (string, error) {
	var response guestExecResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		slog.Warn("deploy-gate: qm guest exec response is not JSON", "err", err)
		return "", fmt.Errorf("parse qm guest exec response: %w", err)
	}
	if response.ExitCode != 0 {
		return "", fmt.Errorf(
			"guest command exited %d (exit code from cat inside the guest)",
			response.ExitCode)
	}
	bootID := strings.TrimSpace(response.OutData)
	if !bootIDPattern.MatchString(bootID) {
		return "", fmt.Errorf("guest returned %q, which is not a boot_id UUID", bootID)
	}
	return bootID, nil
}
