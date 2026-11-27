//go:build linux && firewall

package firewall_test

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

	"goodkind.io/mwan/gateway/integration/internal/testenv"

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
	run      *testenv.BgpRuntimeRun
	tunnel   testenv.TunnelRuntimeTopology
	ordinary netns.NsHandle
	daemon   *testenv.RuntimeDaemon
}

type forcedDSCPFlow struct {
	network string
	dscp    int
	want    string
}

func TestTunnelForcedDSCPDaemonRuntime(t *testing.T) {
	testenv.RunBGPRuntimeIsolated(t, runTunnelForcedDSCPDaemonRuntime)
}

func forcedDSCPNetwork(t *testing.T) string {
	t.Helper()
	tunnelTier := `"targets-v6":["` + testenv.TunnelRuntimeRemoteClient + `"]}},` + forcedDSCPTierLeaf
	underlayTier := `"targets-v6":[]}},` + forcedDSCPTierLeaf
	gateway := `"goodkind-mwan-steering:gateway":"` + testenv.TunnelRuntimeInnerRemote + `",`
	edits := []struct {
		old, replacement string
		count            int
	}{
		{tunnelTier, strings.Replace(tunnelTier, `"tier":0,`, `"tier":3,`, 1), 1},
		{underlayTier, strings.Replace(underlayTier, `"tier":0,`, `"tier":1,`, 1), 2},
		{
			gateway,
			gateway + fmt.Sprintf(
				`"goodkind-mwan-steering:route-metric":%d,`,
				forcedDSCPTunnelMetric,
			),
			1,
		},
		{
			`"fw-mark-prio":200,`,
			fmt.Sprintf(`"fw-mark-prio":200,"forced-dscp":%d,`, forcedDSCPValue),
			1,
		},
		{
			testenv.CombinedInternalEntry,
			testenv.CombinedOrdinaryEntry() + testenv.CombinedInternalEntry,
			1,
		},
	}
	network := testenv.TunnelRuntimeNetwork(true)
	for _, edit := range edits {
		if count := strings.Count(network, edit.old); count != edit.count {
			t.Fatalf("edit target %q occurs %d times, want %d", edit.old, count, edit.count)
		}
		network = strings.ReplaceAll(network, edit.old, edit.replacement)
	}
	return network
}

func buildForcedDSCPTopology(t *testing.T, run *testenv.BgpRuntimeRun) *forcedDSCPRun {
	t.Helper()
	tunnel := testenv.BuildTunnelRuntimeTopology(t, run.Gateway)
	ordinary := testenv.NewRuntimePeer(
		t,
		run.Gateway,
		testenv.CombinedOrdinary,
		testenv.CombinedOrdinaryLink,
		[]string{testenv.CombinedOrdinaryLocalV4 + "/30", testenv.CombinedOrdinaryLocalV6 + "/64"},
		[]string{
			testenv.CombinedOrdinaryPeerV4 + "/30",
			testenv.CombinedOrdinaryPeerV6 + "/64",
			forcedDSCPDestinationV4 + "/32",
			testenv.CombinedDestination + "/128",
		},
		"",
	)
	t.Cleanup(func() { _ = ordinary.Namespace.Close() })
	run.Topology = testenv.BgpRuntimeTopology{
		Gateway: run.Gateway,
		Client:  tunnel.Client,
		Isp:     tunnel.Isp,
		Routers: map[string]netns.NsHandle{
			"endpoint": tunnel.Endpoint,
			"ordinary": ordinary.Namespace,
		},
		Remotes: nil,
	}

	testenv.SetRuntimeNamespace(t, ordinary.Namespace)
	testenv.AddMappedRuntimeRoute(
		t,
		testenv.BgpRuntimeHome,
		testenv.CombinedOrdinaryLocalV6,
		testenv.CombinedOrdinaryLink,
	)
	testenv.RunRuntimeNFT(t, "add", "table", "ip", forcedDSCPCountTable)
	testenv.RunRuntimeNFT(
		t,
		"add",
		"chain",
		"ip",
		forcedDSCPCountTable,
		"input",
		"{ type filter hook input priority 0; }",
	)
	testenv.RunRuntimeNFT(
		t,
		"add",
		"rule",
		"ip",
		forcedDSCPCountTable,
		"input",
		"ip",
		"dscp",
		forcedDSCPLabel,
		"tcp",
		"dport",
		strconv.Itoa(forcedDSCPPort),
		"counter",
	)
	testenv.SetRuntimeNamespace(t, tunnel.Remote)
	testenv.ConfigureRuntimeLink(t, "remote-host", []string{testenv.CombinedDestination + "/128"})
	testenv.SetRuntimeNamespace(t, tunnel.Endpoint)
	testenv.AddMappedRuntimeRoute(
		t,
		testenv.CombinedDestination+"/128",
		testenv.TunnelRuntimeRemoteClient,
		"far-lan",
	)
	testenv.ConfigureRuntimeLink(t, "far-isp", []string{forcedDSCPDestinationV4 + "/32"})
	testenv.SetRuntimeNamespace(t, tunnel.Isp)
	testenv.AddMappedRuntimeRoute(
		t,
		forcedDSCPDestinationV4+"/32",
		testenv.TunnelRuntimeRemote,
		"isp-far",
	)
	testenv.SetRuntimeNamespace(t, tunnel.Alternate)
	testenv.AddMappedRuntimeRoute(
		t,
		forcedDSCPDestinationV4+"/32",
		testenv.TunnelRuntimeAltEndpoint,
		"alt-far",
	)
	testenv.SetRuntimeNamespace(t, run.Gateway)
	testenv.AddCombinedOrdinaryDefaults(t)
	return &forcedDSCPRun{run: run, tunnel: tunnel, ordinary: ordinary.Namespace, daemon: nil}
}

func (c *forcedDSCPRun) path(t *testing.T, flow forcedDSCPFlow) (string, error) {
	t.Helper()
	destinations := map[string]netns.NsHandle{
		testenv.CombinedOrdinary:    c.ordinary,
		testenv.TunnelRuntimeDevice: c.tunnel.Remote,
	}
	address, source := testenv.CombinedDestination, testenv.TunnelRuntimeClientV6
	level, option := unix.IPPROTO_IPV6, unix.IPV6_TCLASS
	if flow.network == "tcp4" {
		destinations = map[string]netns.NsHandle{
			testenv.CombinedOrdinary: c.ordinary,
			forcedDSCPFarEndpoint:    c.tunnel.Endpoint,
		}
		address, source = forcedDSCPDestinationV4, testenv.TunnelRuntimeClientV4
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
	return testenv.RuntimeTransferPath(
		t,
		c.tunnel,
		destinations,
		flow.network,
		destination,
		source,
		control,
	)
}

func (c *forcedDSCPRun) mismatch(t *testing.T, stampedIPv6 string) string {
	t.Helper()
	flows := []forcedDSCPFlow{
		{network: "tcp6", dscp: 0, want: testenv.CombinedOrdinary},
		{network: "tcp6", dscp: forcedDSCPValue, want: stampedIPv6},
		{network: "tcp4", dscp: 0, want: testenv.CombinedOrdinary},
		{network: "tcp4", dscp: forcedDSCPValue, want: testenv.CombinedOrdinary},
	}
	for _, flow := range flows {
		if used, err := c.path(t, flow); err != nil || used != flow.want {
			return fmt.Sprintf(
				"%s with DSCP %d used %q (%v), want %s",
				flow.network,
				flow.dscp,
				used,
				err,
				flow.want,
			)
		}
	}
	return ""
}

func (c *forcedDSCPRun) requirePaths(t *testing.T, phase, stampedIPv6 string) {
	t.Helper()
	testenv.WaitBGPRuntime(
		t,
		c.daemon,
		c.run.Read,
		"the client paths "+phase,
		testenv.TunnelRuntimeRecoverWindow,
		func() bool {
			mismatch := c.mismatch(t, stampedIPv6)
			if mismatch != "" {
				t.Logf("client paths %s repeat: %s", phase, mismatch)
			}
			return mismatch == ""
		},
	)
	if mismatch := c.mismatch(t, stampedIPv6); mismatch != "" {
		t.Fatalf(
			"client paths %s: %s; daemon=%s",
			phase,
			mismatch,
			testenv.TunnelRuntimeLog(t, c.daemon),
		)
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
	testenv.SetRuntimeNamespace(t, c.ordinary)
	defer testenv.SetRuntimeNamespace(t, c.run.Gateway)
	output, err := exec.Command("nft", "list", "table", "ip", forcedDSCPCountTable).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "counter packets") ||
		strings.Contains(string(output), "counter packets 0 ") {
		t.Fatalf(
			"the ordinary provider peer counted no IPv4 TCP packet with DSCP %s: %v: %s",
			forcedDSCPLabel,
			err,
			output,
		)
	}
}

func runTunnelForcedDSCPDaemonRuntime(t *testing.T, run *testenv.BgpRuntimeRun) {
	c := buildForcedDSCPTopology(t, run)
	if err := os.WriteFile(
		filepath.Join(run.NetworkDir, "network.json"),
		[]byte(forcedDSCPNetwork(t)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	c.daemon = run.Start(t, "forced-dscp")
	testenv.WaitRuntimeTable(t, c.daemon, "inet", "filter", 10*time.Second)
	for _, family := range []string{"ipv4", "ipv6"} {
		testenv.WaitTunnelRuntimeFamily(
			t,
			c.daemon,
			run.Read,
			testenv.CombinedOrdinary,
			family,
			"ready",
			"ready",
			testenv.TunnelRuntimeRecoverWindow,
		)
	}
	testenv.WaitTunnelRuntimeFamily(
		t,
		c.daemon,
		run.Read,
		testenv.TunnelRuntimeDevice,
		"ipv6",
		"ready",
		"ready",
		testenv.TunnelRuntimeRecoverWindow,
	)
	mangle := forcedDSCPChain(t, "mangle", "prerouting")
	if !strings.Contains(mangle, "ip6 dscp "+forcedDSCPLabel) ||
		strings.Contains(mangle, "ip dscp") {
		t.Fatalf(
			"the mangle chain lacks the IPv6 DSCP rule or matches the DSCP value on IPv4 packets: %s",
			mangle,
		)
	}
	c.requirePaths(t, "with the tunnel ready", testenv.TunnelRuntimeDevice)
	c.requireStampedIPv4Counted(t)

	testenv.SetTunnelRuntimeFault(t, c.tunnel, true)
	testenv.WaitTunnelRuntimeFamily(
		t,
		c.daemon,
		run.Read,
		testenv.TunnelRuntimeDevice,
		"ipv6",
		"not-ready",
		"not-ready",
		testenv.TunnelRuntimeRecoverWindow,
	)
	c.requirePaths(t, "with the tunnel not ready", testenv.CombinedOrdinary)
	reassigned := false
	steering := forcedDSCPChain(t, "mwan_steer", "prerouting")
	for _, line := range strings.Split(steering, "\n") {
		reassigned = reassigned ||
			strings.Contains(
				line,
				"ip6 saddr",
			) && strings.Contains(line, "meta mark "+forcedDSCPTunnelMark) &&
				strings.Contains(line, "meta mark set "+forcedDSCPOrdinaryMark)
	}
	if !reassigned {
		t.Fatalf(
			"the steering chain has no rule that replaces the tunnel mark with the ordinary provider mark: %s",
			steering,
		)
	}
}
