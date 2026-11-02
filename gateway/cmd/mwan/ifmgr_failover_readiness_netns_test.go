//go:build linux && firewallnetns

package main

import (
	"fmt"
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
	"goodkind.io/mwan/internal/forwardingready"
)

const (
	failoverReadinessChildEnv  = "MWAN_FAILOVER_READINESS_CHILD"
	failoverReadinessBinaryEnv = "MWAN_FAILOVER_READINESS_BINARY"
	failoverUnhealthyAfter     = 2 * time.Second
	failoverPeerIPv6           = "2001:db8:116::2/64"
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
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), failoverReadinessChildEnv+"=1", failoverReadinessBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
}

func runFailoverForwardingReadinessDaemonRuntime(t *testing.T, ipv4Only bool) {
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	root := t.TempDir()
	stateDir := filepath.Join(root, "mwan-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, stateDir, "/var/lib/mwan")
	setRuntimeLoopback(t)
	upstream := newRuntimePeer(t, gateway, "enatt0", "upstream",
		[]string{"198.51.100.1/24", "2001:db8:116::1/64"},
		[]string{"198.51.100.2/24", failoverPeerIPv6}, "")
	defer upstream.namespace.Close()
	socket := filepath.Join(root, "forwarding.sock")
	configPath := filepath.Join(root, "config.toml")
	ipv6Target := `targets_v6 = ["2001:db8:116::2"]`
	ipv6Announcement := `ipv6 = ["::/0"]`
	if ipv4Only {
		ipv6Target = ""
		ipv6Announcement = ""
	}
	configuration := fmt.Sprintf(`[ifmgr]
role = "failover"
reconcile_interval = "200ms"
[ifmgr.iface.enatt0]
[ifmgr.modules.slaac_health]
iface = "enatt0"
[ifmgr.modules.bridge_probe]
iface = "enatt0"
[ifmgr.modules.connectivity_probe]
iface = "enatt0"
targets_v4 = ["198.51.100.2"]
%s
timeout = "100ms"
unhealthy_after = %q
[ifmgr.modules.ra_lost]
iface = "enatt0"
[ifmgr.modules.mainv4]
iface = "enatt0"
[bgp]
enabled = true
[bgp.announce]
ipv4 = ["0.0.0.0/0"]
%s
[bgp.forwarding_readiness]
socket_path = %q
poll_interval_milliseconds = 100
read_timeout_milliseconds = 500
`, ipv6Target, failoverUnhealthyAfter, ipv6Announcement, socket)
	if err := os.WriteFile(configPath, []byte(configuration), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon := startRoleDHCPv4RuntimeDaemon(t, os.Getenv(failoverReadinessBinaryEnv), configPath, root, "failover")
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
	removedAt := time.Now()
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: false})
	if elapsed := time.Since(removedAt); elapsed < failoverUnhealthyAfter {
		t.Fatalf("IPv6 elapsed = %s, want >= %s", elapsed, failoverUnhealthyAfter)
	}

	setRuntimeNamespace(t, upstream.namespace)
	peerAddress.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(peerLink, peerAddress); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	waitOwnedRoleForwarding(t, daemon, socket, forwardingready.State{IPv4: true, IPv6: true})
}
