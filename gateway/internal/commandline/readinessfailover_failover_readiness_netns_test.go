//go:build linux && protocol

package commandline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/internal/forwardingready"
)

const (
	failoverReadinessChildEnv  = "MWAN_FAILOVER_READINESS_CHILD"
	failoverReadinessBinaryEnv = "MWAN_FAILOVER_READINESS_BINARY"
	failoverReadinessTimeout   = 5 * time.Minute
	failoverReadinessKillDelay = 5 * time.Second
	failoverPeerIPv6           = "2001:db8:116::2/64"
	failoverPeerIPv6Address    = "2001:db8:116::2"
	failoverReadinessAgentPort = 50052
	failoverReadinessGatewayV4 = "192.0.2.8/30"
)

func TestFailoverForwardingReadinessDaemonRuntime(t *testing.T) {
	testFailoverForwardingReadiness(t, false)
}

func TestFailoverForwardingReadinessIPv4OnlyDaemonRuntime(t *testing.T) {
	testFailoverForwardingReadiness(t, true)
}

func testFailoverForwardingReadiness(t *testing.T, ipv4Only bool) {
	t.Helper()
	if os.Getenv(failoverReadinessChildEnv) == "1" {
		runFailoverForwardingReadinessDaemonRuntime(t, ipv4Only)
		return
	}
	t.Parallel()
	binary := protocolTestBinary(t)
	childContext, cancel := context.WithTimeout(context.Background(), failoverReadinessTimeout)
	defer cancel()
	child := exec.CommandContext(childContext, os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
		Setpgid:    true,
	}
	child.Cancel = func() error {
		err := syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}

		return err
	}
	child.WaitDelay = failoverReadinessKillDelay
	child.Env = append(
		os.Environ(),
		failoverReadinessChildEnv+"=1",
		failoverReadinessBinaryEnv+"="+binary,
	)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
}

func runFailoverForwardingReadinessDaemonRuntime(t *testing.T, ipv4Only bool) {
	mountPrivateRuntimeDirectories(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root, networkDir, configPath := prepareRuntimeDirectories(t)
	setRuntimeLoopback(t)
	upstream := newRuntimePeer(
		t,
		gateway,
		"enatt0",
		"upstream",
		[]string{"198.51.100.1/24", "2001:db8:116::1/64"},
		[]string{"198.51.100.2/24", failoverPeerIPv6},
		"",
	)
	defer upstream.namespace.Close()
	writeForwardingWANNetwork(
		t,
		networkDir,
		"enatt0",
		"enmwanbr0",
		"192.0.2.0/29",
		"2001:db8:b01::/60",
		"198.51.100.2",
		failoverPeerIPv6Address,
		!ipv4Only,
		3,
	)
	addForwardingWANDefaults(t, "enatt0", "198.51.100.2", failoverPeerIPv6Address)
	addForwardingWANSupportLinks(t)
	socket := filepath.Join(root, "forwarding.sock")
	ipv6Announcement := `ipv6 = ["::/0"]`
	if ipv4Only {
		ipv6Announcement = ""
	}
	configuration := fmt.Sprintf(
		`[ifmgr]
role = "wan"
reconcile_interval = "200ms"
[ifmgr.iface.enmwanbr0]
[ifmgr.modules.links]
state_file = %q
[ifmgr.modules.addresses]
state_file = %q
[ifmgr.modules.autoconfiguration]
state_file = %q
[ifmgr.modules.bgp_sessions]
state_file = %q
[wanconfig]
publish = true
[bgp]
enabled = true
[bgp.announce]
ipv4 = ["0.0.0.0/0"]
%s
[bgp.forwarding_readiness]
socket_path = %q
poll_interval_milliseconds = 100
read_timeout_milliseconds = 500
`,
		filepath.Join(root, "links.json"),
		filepath.Join(root, "addresses.json"),
		filepath.Join(root, "autoconfiguration.json"),
		filepath.Join(root, "bgp-sessions.json"),
		ipv6Announcement,
		socket,
	)
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startRuntimeDaemon(
		t,
		os.Getenv(failoverReadinessBinaryEnv),
		configPath,
		root,
		"wan-readiness",
	)
	defer stopRuntimeDaemon(t, daemon)
	if ipv4Only {
		waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: false})
		return
	}
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: true})

	setRuntimeNamespace(t, upstream.namespace)
	peerAddress, err := netlink.ParseAddr(failoverPeerIPv6)
	if err != nil {
		t.Fatal(err)
	}
	peerLink, err := netlink.LinkByName("upstream")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrDel(peerLink, peerAddress); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: false})

	setRuntimeNamespace(t, upstream.namespace)
	peerAddress.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(peerLink, peerAddress); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: true})
}

func addForwardingWANSupportLinks(t *testing.T) {
	t.Helper()
	for _, name := range []string{"enmwanbr0", "enmgmt0"} {
		if err := netlink.LinkAdd(&netlink.Dummy{
			LinkAttrs: netlink.LinkAttrs{Name: name},
		}); err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
}

func addForwardingWANDefaults(t *testing.T, iface, gatewayV4, gatewayV6 string) {
	t.Helper()
	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	for _, gateway := range []string{gatewayV4, gatewayV6} {
		if err := netlink.RouteReplace(&netlink.Route{
			LinkIndex: link.Attrs().Index,
			Gw:        net.ParseIP(gateway),
			Table:     unix.RT_TABLE_MAIN,
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func writeForwardingWANNetwork(
	t *testing.T,
	directory string,
	iface string,
	internalIface string,
	internalNetV4 string,
	internalPrefixV6 string,
	targetV4 string,
	targetV6 string,
	ipv6Enabled bool,
	failureThreshold int,
) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "yang", "instances", "network-min.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	var selected []map[string]json.RawMessage
	for _, entry := range entries {
		name := string(entry["name"])
		if name == `"enatt0"` {
			encodedName, err := json.Marshal(iface)
			if err != nil {
				t.Fatal(err)
			}
			entry["name"] = encodedName
			entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"external"`)
			delete(entry, "goodkind-mwan-steering:link-files")
			delete(entry, "goodkind-mwan-steering:link")
			if !ipv6Enabled {
				delete(entry, "ietf-ip:ipv6")
			}
			var wan map[string]json.RawMessage
			if err := json.Unmarshal(entry["goodkind-mwan-steering:wan"], &wan); err != nil {
				t.Fatal(err)
			}
			healthConfig := forwardingWANHealth{
				Enabled:           true,
				PingCount:         1,
				SuccessThreshold:  1,
				FailureThreshold:  failureThreshold,
				RecoveryThreshold: 1,
				CheckInterval:     1,
				TargetsV4:         []string{targetV4},
				TargetsV6:         []string{},
			}
			if ipv6Enabled {
				healthConfig.TargetsV6 = []string{targetV6}
			}
			health, err := json.Marshal(healthConfig)
			if err != nil {
				t.Fatal(err)
			}
			wan["health"] = health
			entry["goodkind-mwan-steering:wan"], err = json.Marshal(wan)
			if err != nil {
				t.Fatal(err)
			}
			selected = append(selected, entry)
			continue
		}
		if name == `"enmwanbr0"` || name == `"enmgmt0"` {
			selected = append(selected, entry)
		}
	}
	if internalIface != "enmwanbr0" && internalIface != "enmgmt0" {
		encodedName, err := json.Marshal(internalIface)
		if err != nil {
			t.Fatal(err)
		}
		selected = append(
			selected,
			map[string]json.RawMessage{
				"name": encodedName,
				"type": json.RawMessage(`"iana-if-type:other"`),
			},
		)
	}
	var steeringGroup map[string]json.RawMessage
	if err := json.Unmarshal(
		interfaces["goodkind-mwan-steering:steering-group"],
		&steeringGroup,
	); err != nil {
		t.Fatal(err)
	}
	steeringGroup["health"] = json.RawMessage(`{"probe-timeout":100}`)
	var routes map[string]json.RawMessage
	if err := json.Unmarshal(steeringGroup["routes"], &routes); err != nil {
		t.Fatal(err)
	}
	routes["internal-iface"], err = json.Marshal(internalIface)
	if err != nil {
		t.Fatal(err)
	}
	routes["internal-net-v4"], err = json.Marshal(internalNetV4)
	if err != nil {
		t.Fatal(err)
	}
	steeringGroup["routes"], err = json.Marshal(routes)
	if err != nil {
		t.Fatal(err)
	}
	var translation map[string]json.RawMessage
	if err := json.Unmarshal(steeringGroup["translation"], &translation); err != nil {
		t.Fatal(err)
	}
	translation["internal-prefix"], err = json.Marshal(internalPrefixV6)
	if err != nil {
		t.Fatal(err)
	}
	steeringGroup["translation"], err = json.Marshal(translation)
	if err != nil {
		t.Fatal(err)
	}
	steeringGroup["firewall"] = json.RawMessage(
		fmt.Sprintf(
			`{"management-interface":%q,"management-service":[{"protocol":"tcp","port":22},{"protocol":"tcp","port":%d,"allowed-source":[%q,%q]}],"pinned-set-v4-name":"att_pinned_v4","pinned-set-v6-name":"att_pinned_v6"}`,
			internalIface,
			failoverReadinessAgentPort,
			internalNetV4,
			failoverReadinessGatewayV4,
		),
	)
	interfaces["goodkind-mwan-steering:steering-group"], err = json.Marshal(steeringGroup)
	if err != nil {
		t.Fatal(err)
	}
	encodedEntries, err := json.Marshal(selected)
	if err != nil {
		t.Fatal(err)
	}
	interfaces["interface"] = encodedEntries
	encodedInterfaces, err := json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"] = encodedInterfaces
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "network.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

type forwardingWANHealth struct {
	Enabled           bool     `json:"enabled"`
	PingCount         int      `json:"ping-count"`
	SuccessThreshold  int      `json:"success-threshold"`
	FailureThreshold  int      `json:"failure-threshold"`
	RecoveryThreshold int      `json:"recovery-threshold"`
	CheckInterval     int      `json:"check-interval"`
	TargetsV4         []string `json:"targets-v4"`
	TargetsV6         []string `json:"targets-v6"`
}
