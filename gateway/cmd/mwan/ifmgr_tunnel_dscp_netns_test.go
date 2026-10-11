//go:build linux && firewallnetns

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	forcedDSCPValue         = 8
	forcedDSCPLabel         = "cs1"
	forcedDSCPPort          = 18611
	forcedDSCPDestinationV4 = "198.51.100.129"
	forcedDSCPFarEndpoint   = "far endpoint"
	forcedDSCPCountTable    = "forceddscpcount"
	forcedDSCPTierLeaf      = `"goodkind-mwan-steering:steering":{"tier":0,`
	forcedDSCPTunnelMetric  = 2048
	forcedDSCPTunnelMark    = "0x00000002"
	forcedDSCPOrdinaryMark  = "0x00000005"
)

type forcedDSCPRun struct {
	run      *bgpRuntimeRun
	tunnel   tunnelRuntimeTopology
	ordinary netns.NsHandle
	daemon   *runtimeDaemon
}

type forcedDSCPFlow struct {
	network string
	dscp    int
	want    string
}

func TestTunnelForcedDSCPDaemonRuntime(t *testing.T) {
	runBGPRuntimeIsolated(t, runTunnelForcedDSCPDaemonRuntime)
}

func forcedDSCPNetwork(t *testing.T) string {
	t.Helper()
	tunnelTier := `"targets-v6":["` + tunnelRuntimeRemoteClient + `"]}},` + forcedDSCPTierLeaf
	underlayTier := `"targets-v6":[]}},` + forcedDSCPTierLeaf
	gateway := `"goodkind-mwan-steering:gateway":"` + tunnelRuntimeInnerRemote + `",`
	edits := []struct {
		old, replacement string
		count            int
	}{
		{tunnelTier, strings.Replace(tunnelTier, `"tier":0,`, `"tier":3,`, 1), 1},
		{underlayTier, strings.Replace(underlayTier, `"tier":0,`, `"tier":1,`, 1), 2},
		{gateway, gateway + fmt.Sprintf(`"goodkind-mwan-steering:route-metric":%d,`, forcedDSCPTunnelMetric), 1},
		{`"fw-mark-prio":200,`, fmt.Sprintf(`"fw-mark-prio":200,"forced-dscp":%d,`, forcedDSCPValue), 1},
		{combinedInternalEntry, combinedOrdinaryEntry() + combinedInternalEntry, 1},
	}
	network := tunnelRuntimeNetwork(true)
	for _, edit := range edits {
		if count := strings.Count(network, edit.old); count != edit.count {
			t.Fatalf("edit target %q occurs %d times, want %d", edit.old, count, edit.count)
		}
		network = strings.ReplaceAll(network, edit.old, edit.replacement)
	}
	return network
}

func buildForcedDSCPTopology(t *testing.T, run *bgpRuntimeRun) *forcedDSCPRun {
	t.Helper()
	tunnel := buildTunnelRuntimeTopology(t, run.gateway)
	ordinary := newRuntimePeer(t, run.gateway, combinedOrdinary, combinedOrdinaryLink,
		[]string{combinedOrdinaryLocalV4 + "/30", combinedOrdinaryLocalV6 + "/64"},
		[]string{combinedOrdinaryPeerV4 + "/30", combinedOrdinaryPeerV6 + "/64", forcedDSCPDestinationV4 + "/32", combinedDestination + "/128"}, "")
	t.Cleanup(func() { _ = ordinary.namespace.Close() })
	run.topology = bgpRuntimeTopology{
		gateway: run.gateway, client: tunnel.client, isp: tunnel.isp,
		routers: map[string]netns.NsHandle{"endpoint": tunnel.endpoint, "ordinary": ordinary.namespace}, remotes: nil,
	}

	setRuntimeNamespace(t, ordinary.namespace)
	addMappedRuntimeRoute(t, bgpRuntimeHome, combinedOrdinaryLocalV6, combinedOrdinaryLink)
	runRuntimeNFT(t, "add", "table", "ip", forcedDSCPCountTable)
	runRuntimeNFT(t, "add", "chain", "ip", forcedDSCPCountTable, "input", "{ type filter hook input priority 0; }")
	runRuntimeNFT(t, "add", "rule", "ip", forcedDSCPCountTable, "input", "ip", "dscp", forcedDSCPLabel,
		"tcp", "dport", strconv.Itoa(forcedDSCPPort), "counter")
	setRuntimeNamespace(t, tunnel.remote)
	configureRuntimeLink(t, "remote-host", []string{combinedDestination + "/128"})
	setRuntimeNamespace(t, tunnel.endpoint)
	addMappedRuntimeRoute(t, combinedDestination+"/128", tunnelRuntimeRemoteClient, "far-lan")
	configureRuntimeLink(t, "far-isp", []string{forcedDSCPDestinationV4 + "/32"})
	setRuntimeNamespace(t, tunnel.isp)
	addMappedRuntimeRoute(t, forcedDSCPDestinationV4+"/32", tunnelRuntimeRemote, "isp-far")
	setRuntimeNamespace(t, tunnel.alternate)
	addMappedRuntimeRoute(t, forcedDSCPDestinationV4+"/32", tunnelRuntimeAltEndpoint, "alt-far")
	setRuntimeNamespace(t, run.gateway)
	addCombinedOrdinaryDefaults(t)
	return &forcedDSCPRun{run: run, tunnel: tunnel, ordinary: ordinary.namespace, daemon: nil}
}

func (c *forcedDSCPRun) path(t *testing.T, flow forcedDSCPFlow) (string, error) {
	t.Helper()
	destinations := map[string]netns.NsHandle{combinedOrdinary: c.ordinary, tunnelRuntimeDevice: c.tunnel.remote}
	address, source := combinedDestination, tunnelRuntimeClientV6
	level, option := unix.IPPROTO_IPV6, unix.IPV6_TCLASS
	if flow.network == "tcp4" {
		destinations = map[string]netns.NsHandle{combinedOrdinary: c.ordinary, forcedDSCPFarEndpoint: c.tunnel.endpoint}
		address, source = forcedDSCPDestinationV4, tunnelRuntimeClientV4
		level, option = unix.IPPROTO_IP, unix.IP_TOS
	}
	control := func(_, _ string, connection syscall.RawConn) error {
		var optionErr error
		controlErr := connection.Control(func(descriptor uintptr) {
			optionErr = unix.SetsockoptInt(int(descriptor), level, option, flow.dscp<<2)
		})
		return errors.Join(controlErr, optionErr)
	}
	destination := net.JoinHostPort(address, strconv.Itoa(forcedDSCPPort))
	return runtimeTransferPath(t, c.tunnel, destinations, flow.network, destination, source, control)
}

func (c *forcedDSCPRun) mismatch(t *testing.T, stampedIPv6 string) string {
	t.Helper()
	flows := []forcedDSCPFlow{
		{network: "tcp6", dscp: 0, want: combinedOrdinary},
		{network: "tcp6", dscp: forcedDSCPValue, want: stampedIPv6},
		{network: "tcp4", dscp: 0, want: combinedOrdinary},
		{network: "tcp4", dscp: forcedDSCPValue, want: combinedOrdinary},
	}
	for _, flow := range flows {
		if used, err := c.path(t, flow); err != nil || used != flow.want {
			return fmt.Sprintf("%s with DSCP %d used %q (%v), want %s", flow.network, flow.dscp, used, err, flow.want)
		}
	}
	return ""
}

func (c *forcedDSCPRun) requirePaths(t *testing.T, phase, stampedIPv6 string) {
	t.Helper()
	waitBGPRuntime(t, c.daemon, c.run.read, "the client paths "+phase, tunnelRuntimeRecoverWindow, func() bool {
		mismatch := c.mismatch(t, stampedIPv6)
		if mismatch != "" {
			t.Logf("client paths %s repeat: %s", phase, mismatch)
		}
		return mismatch == ""
	})
	if mismatch := c.mismatch(t, stampedIPv6); mismatch != "" {
		t.Fatalf("client paths %s: %s; daemon=%s", phase, mismatch, tunnelRuntimeLog(t, c.daemon))
	}
}

func forcedDSCPChain(t *testing.T, table, chain string) string {
	t.Helper()
	output, err := exec.Command("nft", "list", "chain", "inet", table, chain).CombinedOutput()
	if err != nil {
		t.Fatalf("read the %s %s chain: %v: %s", table, chain, err, output)
	}
	return string(output)
}

func (c *forcedDSCPRun) requireStampedIPv4Counted(t *testing.T) {
	t.Helper()
	setRuntimeNamespace(t, c.ordinary)
	defer setRuntimeNamespace(t, c.run.gateway)
	output, err := exec.Command("nft", "list", "table", "ip", forcedDSCPCountTable).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "counter packets") || strings.Contains(string(output), "counter packets 0 ") {
		t.Fatalf("the ordinary provider peer counted no IPv4 TCP packet with DSCP %s: %v: %s", forcedDSCPLabel, err, output)
	}
}

func runTunnelForcedDSCPDaemonRuntime(t *testing.T, run *bgpRuntimeRun) {
	c := buildForcedDSCPTopology(t, run)
	if err := os.WriteFile(filepath.Join(run.networkDir, "network.json"), []byte(forcedDSCPNetwork(t)), 0o600); err != nil {
		t.Fatal(err)
	}
	c.daemon = run.start(t, "forced-dscp")
	waitRuntimeTable(t, c.daemon, "inet", "filter", 10*time.Second)
	for _, family := range []string{"ipv4", "ipv6"} {
		waitTunnelRuntimeFamily(t, c.daemon, run.read, combinedOrdinary, family, "ready", "ready", tunnelRuntimeRecoverWindow)
	}
	waitTunnelRuntimeFamily(t, c.daemon, run.read, tunnelRuntimeDevice, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
	mangle := forcedDSCPChain(t, "mangle", "prerouting")
	if !strings.Contains(mangle, "ip6 dscp "+forcedDSCPLabel) || strings.Contains(mangle, "ip dscp") {
		t.Fatalf("the mangle chain lacks the IPv6 DSCP rule or matches the DSCP value on IPv4 packets: %s", mangle)
	}
	c.requirePaths(t, "with the tunnel ready", tunnelRuntimeDevice)
	c.requireStampedIPv4Counted(t)

	setTunnelRuntimeFault(t, c.tunnel, true)
	waitTunnelRuntimeFamily(t, c.daemon, run.read, tunnelRuntimeDevice, "ipv6", "not-ready", "not-ready", tunnelRuntimeRecoverWindow)
	c.requirePaths(t, "with the tunnel not ready", combinedOrdinary)
	reassigned := false
	steering := forcedDSCPChain(t, "mwan_steer", "prerouting")
	for _, line := range strings.Split(steering, "\n") {
		reassigned = reassigned || strings.Contains(line, "ip6 saddr") && strings.Contains(line, "meta mark "+forcedDSCPTunnelMark) &&
			strings.Contains(line, "meta mark set "+forcedDSCPOrdinaryMark)
	}
	if !reassigned {
		t.Fatalf("the steering chain has no rule that replaces the tunnel mark with the ordinary provider mark: %s", steering)
	}
}
