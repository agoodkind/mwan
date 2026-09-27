package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"goodkind.io/mwan/internal/ops"
)

type downstreamProbeConfig struct {
	opnsenseVMID int
	ipv4Source   netip.Addr
	ipv4NextHop  netip.Addr
	ipv6Source   netip.Addr
	ipv6NextHop  netip.Addr
}

const (
	downstreamHTTPSConnectTimeout = 3 * time.Second
	downstreamHTTPSRequestTimeout = 8 * time.Second
	downstreamGuestExecTimeout    = downstreamHTTPSRequestTimeout + 2*time.Second
)

type downstreamProbeFile struct {
	OPNsenseVMID int    `json:"opnsense_vmid"`
	IPv4Source   string `json:"ipv4_source"`
	IPv4NextHop  string `json:"ipv4_next_hop"`
	IPv6Source   string `json:"ipv6_source"`
	IPv6NextHop  string `json:"ipv6_next_hop"`
}

func readDownstreamProbeConfig(
	path string, families requiredEgressFamilies,
) (downstreamProbeConfig, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		slog.Warn("open downstream probe config directory failed", "path", path, "err", err)
		return downstreamProbeConfig{}, fmt.Errorf("open probe config directory %s: %w", path, err)
	}
	defer root.Close()
	file, err := root.Open(filepath.Base(path))
	if err != nil {
		return downstreamProbeConfig{}, fmt.Errorf("open probe config %s: %w", path, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var input downstreamProbeFile
	if err := decoder.Decode(&input); err != nil {
		return downstreamProbeConfig{}, fmt.Errorf("decode probe config %s: %w", path, err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); err != io.EOF {
		return downstreamProbeConfig{}, fmt.Errorf("probe config %s must contain one JSON object", path)
	}
	if input.OPNsenseVMID <= 0 {
		return downstreamProbeConfig{}, fmt.Errorf("probe config %s: opnsense_vmid must be positive", path)
	}
	probe := downstreamProbeConfig{opnsenseVMID: input.OPNsenseVMID}
	if families.ipv4 {
		probe.ipv4Source, err = parseProbeAddress("ipv4_source", input.IPv4Source, true)
		if err != nil {
			return downstreamProbeConfig{}, err
		}
		probe.ipv4NextHop, err = parseProbeAddress("ipv4_next_hop", input.IPv4NextHop, true)
		if err != nil {
			return downstreamProbeConfig{}, err
		}
	}
	if families.ipv6 {
		probe.ipv6Source, err = parseProbeAddress("ipv6_source", input.IPv6Source, false)
		if err != nil {
			return downstreamProbeConfig{}, err
		}
		probe.ipv6NextHop, err = parseProbeAddress("ipv6_next_hop", input.IPv6NextHop, false)
		if err != nil {
			return downstreamProbeConfig{}, err
		}
	}
	return probe, nil
}

func runCheckDownstreamEgress(ctx context.Context, deps deployGateDeps, args []string) int {
	if len(args) != 2 {
		printDeployGateUsage()
		return exitDeployGateUsage
	}
	families, err := parseRequiredEgressFamilies(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan deploy-gate: %v\n", err)
		return exitDeployGateUsage
	}
	probe, err := readDownstreamProbeConfig(args[1], families)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan deploy-gate: %v\n", err)
		return exitDeployGateUsage
	}
	return checkDownstreamEgress(ctx, deps, probe, families)
}

func parseProbeAddress(field string, value string, ipv4 bool) (netip.Addr, error) {
	address, err := netip.ParseAddr(value)
	if err != nil || !address.IsGlobalUnicast() || address.Is4() != ipv4 {
		return netip.Addr{}, fmt.Errorf("probe config %s must be a unicast address of the required family", field)
	}
	return address, nil
}

func checkDownstreamEgress(
	ctx context.Context, deps deployGateDeps, probe downstreamProbeConfig,
	families requiredEgressFamilies,
) int {
	ok, report := deps.probeDownstream(ctx, probe, families)
	fmt.Fprintf(deps.out, "egress probe: %s\n", report)
	if ok {
		return exitDeployGateOK
	}
	fmt.Fprintln(deps.out, "required egress family failed")
	return exitDeployGateFailed
}

func probeDownstreamEgressRound(
	ctx context.Context, probe downstreamProbeConfig, families requiredEgressFamilies,
) (bool, string) {
	v6 := "not-required"
	v4 := "not-required"
	var failures []string
	if families.ipv6 {
		err := probeDownstreamFamily(ctx, probe.opnsenseVMID, false,
			deployGateTargetV6, probe.ipv6Source, probe.ipv6NextHop)
		v6 = yesNo(err == nil)
		if err != nil {
			failures = append(failures, fmt.Sprintf("ipv6_error=%q", err))
		}
	}
	if families.ipv4 {
		err := probeDownstreamFamily(ctx, probe.opnsenseVMID, true,
			deployGateTargetV4, probe.ipv4Source, probe.ipv4NextHop)
		v4 = yesNo(err == nil)
		if err != nil {
			failures = append(failures, fmt.Sprintf("ipv4_error=%q", err))
		}
	}
	report := fmt.Sprintf("ipv6=%s ipv4=%s", v6, v4)
	if len(failures) > 0 {
		report += " " + strings.Join(failures, " ")
	}
	return len(failures) == 0, report
}

func probeDownstreamFamily(
	ctx context.Context, vmid int, ipv4 bool,
	target netip.Addr, source netip.Addr, expectedHop netip.Addr,
) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "downstream egress probe failed", "vmid", vmid,
				"target", target, "source", source, "err", resultErr)
		}
	}()
	routeCommand := []string{"/sbin/route", "-n", "get"}
	if !ipv4 {
		routeCommand = append(routeCommand, "-inet6")
	}
	output, err := readGuestProbeCommand(ctx, vmid,
		append(routeCommand, target.String())...)
	if err != nil {
		return fmt.Errorf("route get %s: %w", target, err)
	}
	hop, err := readRouteGateway(output)
	if err != nil {
		return fmt.Errorf("route get %s: %w", target, err)
	}
	if hop != expectedHop {
		return fmt.Errorf("route get %s: next hop %s, expected %s", target, hop, expectedHop)
	}
	family := "-6"
	if ipv4 {
		family = "-4"
	}
	url := "https://" + net.JoinHostPort(target.String(), "443") + "/"
	// QEMU omits out-truncated when a command prints nothing. The HTTP status
	// keeps the guest response parser's truncation check available.
	_, err = readGuestProbeCommand(ctx, vmid,
		"/usr/local/bin/curl", family, "--noproxy", "*", "--interface", source.String(),
		"--connect-timeout", strconv.Itoa(int(downstreamHTTPSConnectTimeout.Seconds())),
		"--max-time", strconv.Itoa(int(downstreamHTTPSRequestTimeout.Seconds())),
		"-fsS", "-o", "/dev/null", "-w", "%{http_code}", url)
	if err != nil {
		return fmt.Errorf("HTTPS %s from %s: %w", target, source, err)
	}
	return nil
}

func readRouteGateway(output string) (netip.Addr, error) {
	var gateway netip.Addr
	for line := range strings.SplitSeq(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != "gateway:" {
			continue
		}
		if len(fields) != 2 || gateway.IsValid() {
			return netip.Addr{}, fmt.Errorf("route output has an invalid gateway line")
		}
		var err error
		gateway, err = netip.ParseAddr(fields[1])
		if err != nil {
			slog.Warn("downstream route gateway is invalid", "gateway", fields[1], "err", err)
			return netip.Addr{}, fmt.Errorf("route output gateway %q: %w", fields[1], err)
		}
	}
	if !gateway.IsValid() {
		return netip.Addr{}, fmt.Errorf("route output has no gateway")
	}
	return gateway, nil
}

type guestProbeResponse struct {
	Exited       json.RawMessage `json:"exited"`
	ExitCode     *int            `json:"exitcode"`
	OutData      string          `json:"out-data"`
	OutTruncated json.RawMessage `json:"out-truncated"`
}

func readGuestProbeCommand(ctx context.Context, vmid int, command ...string) (string, error) {
	raw, err := ops.GuestExecViaQm(ctx,
		downstreamGuestExecTimeout+5*time.Second, downstreamGuestExecTimeout,
		vmid, command...)
	if err != nil {
		slog.WarnContext(ctx, "downstream guest command failed", "vmid", vmid, "err", err)
		return "", fmt.Errorf("qm guest exec %d: %w", vmid, err)
	}
	return readGuestProbeResponse(raw)
}

func readGuestProbeResponse(raw []byte) (string, error) {
	var response guestProbeResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		slog.Warn("downstream guest response is invalid", "err", err)
		return "", fmt.Errorf("parse qm guest exec response: %w", err)
	}
	exited, err := parseGuestProbeFlag(response.Exited)
	if err != nil || !exited || response.ExitCode == nil {
		return "", fmt.Errorf("guest command did not report a complete exit")
	}
	truncated, err := parseGuestProbeFlag(response.OutTruncated)
	if err != nil || truncated {
		return "", fmt.Errorf("guest command output was truncated or invalid")
	}
	if *response.ExitCode != 0 {
		return "", fmt.Errorf("guest command exited %d", *response.ExitCode)
	}
	return response.OutData, nil
}

func parseGuestProbeFlag(raw json.RawMessage) (bool, error) {
	if len(raw) == 0 {
		return false, fmt.Errorf("missing guest agent flag")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number != 0, nil
	}
	return false, fmt.Errorf("invalid guest agent flag %q", bytes.TrimSpace(raw))
}
