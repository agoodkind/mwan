//go:build linux && firewallnetns

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	dhcpRenamedLinkChildEnv  = "MWAN_DHCP_RENAMED_LINK_CHILD"
	dhcpRenamedLinkBinaryEnv = "MWAN_DHCP_RENAMED_LINK_BINARY"
	dhcpRenamedLinkAddress   = "198.51.100.101/24"
	dhcpRenamedLinkForeign   = "203.0.113.9/24"
)

type dhcpRecoveryLinkChange int

const (
	dhcpRecoveryRename dhcpRecoveryLinkChange = iota
	dhcpRecoveryRenameLateReplacement
	dhcpRecoveryDeleteForeignAddress
	dhcpRecoveryRenameFastRebind
	dhcpRecoveryRenameBeforeStart
	dhcpRecoveryRenameAfterBound
	dhcpRecoveryRenameAfterBoundLongLease
	dhcpRecoveryFlapAfterBoundLongLease
)

func TestOOBDHCPv4RecoveryWithdrawsRenamedLinkAddress(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRename)
}

func TestOOBDHCPv4RecoveryWithdrawsRenamedLinkAddressAfterLateReplacement(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameLateReplacement)
}

func TestOOBDHCPv4RecoveryPreservesForeignAddressOnRecreatedLink(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryDeleteForeignAddress)
}

func TestOOBDHCPv4RecoveryRenamedLinkFastRebind(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameFastRebind)
}

func TestOOBDHCPv4RecoveryRenamedLinkBeforeStart(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameBeforeStart)
}

func TestOOBDHCPv4BoundRenamedLink(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameAfterBound)
}

func TestOOBDHCPv4BoundRenamedLinkLongLease(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryRenameAfterBoundLongLease)
}

func TestOOBDHCPv4BoundLinkFlapKeepsLease(t *testing.T) {
	testOOBDHCPv4RecoveryLinkChange(t, dhcpRecoveryFlapAfterBoundLongLease)
}

func renameDHCPRecoveryLink(t *testing.T, oldLink netlink.Link) {
	t.Helper()
	if err := netlink.LinkSetDown(oldLink); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetName(oldLink, "enatt0-old"); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(oldLink); err != nil {
		t.Fatal(err)
	}
	foreignAddress, err := netlink.ParseAddr(dhcpRenamedLinkForeign)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(oldLink, foreignAddress); err != nil {
		t.Fatal(err)
	}
}

func assertDHCPRenamedLinkForeignAddress(t *testing.T, oldLink netlink.Link) {
	t.Helper()
	addresses, err := netlink.AddrList(oldLink, netlink.FAMILY_V4)
	if err != nil {
		t.Fatal(err)
	}
	foreignPresent := false
	for _, address := range addresses {
		foreignPresent = foreignPresent || address.IPNet.String() == dhcpRenamedLinkForeign
	}
	if !foreignPresent {
		t.Fatalf("foreignPresent = false: addresses=%v", addresses)
	}
}

func testOOBDHCPv4RecoveryLinkChange(t *testing.T, change dhcpRecoveryLinkChange) {
	if os.Getenv(dhcpRenamedLinkChildEnv) == "1" {
		runOOBDHCPv4RecoveryLinkChange(t, change)
		return
	}
	binary := protocolTestBinary(t)
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
	child.Env = append(os.Environ(), dhcpRenamedLinkChildEnv+"=1", dhcpRenamedLinkBinaryEnv+"="+binary)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("exec %s: %v: %s", t.Name(), err, output)
	}
}

func runOOBDHCPv4RecoveryLinkChange(t *testing.T, change dhcpRecoveryLinkChange) {
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
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer downstream.namespace.Close()
	provider := newRuntimePeer(t, gateway, "enatt0", "wan-vlan", nil, []string{"198.51.100.2/24"}, "")
	defer provider.namespace.Close()
	leaseSeconds := 600
	if change == dhcpRecoveryRenameLateReplacement || change == dhcpRecoveryRenameAfterBound {
		leaseSeconds = 40
	}
	setRuntimeNamespace(t, provider.namespace)
	service := startDHCPv4RuntimeKea(t, root, false, leaseSeconds, "198.51.100.2", "198.51.100.100", true)
	serviceRunning := true
	defer func() {
		if serviceRunning {
			setRuntimeNamespace(t, provider.namespace)
			stopProtocolServices(t, []protocolService{service})
		}
	}()
	setRuntimeNamespace(t, gateway)
	leaseDirectory := filepath.Join(root, "leases")
	if err := os.Mkdir(leaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[ifmgr]\nrole = \"oob\"\nreconcile_interval = \"1h\"\nlease_directory = %q\n[ifmgr.iface.enatt0]\ndhcp_v4 = true\n[ifmgr.modules.oobv6]\niface = \"enatt0\"\noob_addr = \"2001:db8::100/128\"\noob_table_id = 100\nmanage_slaac_source_rule = false\n[ifmgr.modules.oobv4]\niface = \"enatt0\"\noob_table_id = 100\n[ifmgr.modules.ra_lost]\niface = \"enatt0\"\n[[ifmgr.modules.policy_rules.rule]]\nfamily = \"ipv4\"\npriority = 100\nfrom = \"192.0.2.2/32\"\ntable_id = 100\n", leaseDirectory)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := os.Getenv(dhcpRenamedLinkBinaryEnv)
	daemon := startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	waitRoleDHCPv4Route(t, daemon, 100, true)
	waitDHCPRecoveryRecord(t, daemon, filepath.Join(leaseDirectory, "roles"))
	if change == dhcpRecoveryFlapAfterBoundLongLease {
		flappedLink, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		logBefore := runtimeDaemonLog(t, daemon)
		if err := netlink.LinkSetDown(flappedLink); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(flappedLink); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Second)
		assertRuntimeDaemonRunning(t, daemon)
		logAfter := runtimeDaemonLog(t, daemon)
		for _, entry := range []string{"state=EXPIRED", "dhcp: DISCOVER", "state=RENEWING", "dhcp: renewal attempt failed"} {
			if before, after := strings.Count(logBefore, entry), strings.Count(logAfter, entry); after != before {
				t.Fatalf("%s count = %d, want %d: %s", entry, after, before, runtimeLogTail(t, daemon, 30))
			}
		}
		waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
		return
	}
	if change == dhcpRecoveryRenameAfterBound || change == dhcpRecoveryRenameAfterBoundLongLease {
		boundLink, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		renameDHCPRecoveryLink(t, boundLink)
		replacement := newRuntimePeer(t, gateway, "enatt0", "wan-new", nil, []string{"198.51.100.2/24"}, "")
		defer replacement.namespace.Close()
		waitDHCPRenamedLinkAddressRemoved(t, daemon, boundLink.Attrs().Index)
		assertDHCPRenamedLinkForeignAddress(t, boundLink)
		return
	}
	stopRuntimeDaemon(t, daemon)
	setRuntimeNamespace(t, provider.namespace)
	stopProtocolServices(t, []protocolService{service})
	serviceRunning = false
	setRuntimeNamespace(t, gateway)

	if change == dhcpRecoveryRenameBeforeStart {
		absentLink, err := netlink.LinkByName("enatt0")
		if err != nil {
			t.Fatal(err)
		}
		renameDHCPRecoveryLink(t, absentLink)
		daemon = startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
		time.Sleep(3 * time.Second)
		assertRuntimeDaemonRunning(t, daemon)
		replacement := newRuntimePeer(t, gateway, "enatt0", "wan-new", nil, []string{"198.51.100.2/24"}, "")
		defer replacement.namespace.Close()
		waitDHCPRenamedLinkAddressRemoved(t, daemon, absentLink.Attrs().Index)
		assertDHCPRenamedLinkForeignAddress(t, absentLink)
		return
	}

	daemon = startRoleDHCPv4RuntimeDaemon(t, binary, configPath, root, "oob")
	waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
	if change == dhcpRecoveryRenameLateReplacement {
		waitDHCPv4RuntimeState(t, daemon, "dhcp: restart request failed", 20*time.Second)
	}
	oldLink, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if change == dhcpRecoveryDeleteForeignAddress {
		if err := netlink.LinkDel(oldLink); err != nil {
			t.Fatal(err)
		}
		replacement := newRuntimePeer(t, gateway, "enatt0", "wan-new", []string{dhcpRenamedLinkAddress}, nil, "")
		defer replacement.namespace.Close()
		waitDHCPv4RuntimeState(t, daemon, "state=EXPIRED", 40*time.Second)
		time.Sleep(2 * time.Second)
		assertRuntimeDaemonRunning(t, daemon)
		waitStaticRuntimeAddress(t, daemon, "enatt0", dhcpRenamedLinkAddress, true)
		return
	}
	renameDHCPRecoveryLink(t, oldLink)
	if change == dhcpRecoveryRenameLateReplacement {
		waitDHCPv4RuntimeState(t, daemon, "state=EXPIRED", 60*time.Second)
	}
	replacement := newRuntimePeer(t, gateway, "enatt0", "wan-new", nil, []string{"198.51.100.2/24"}, "")
	defer replacement.namespace.Close()
	if change == dhcpRecoveryRenameFastRebind {
		setRuntimeNamespace(t, replacement.namespace)
		server, err := server4.NewServer("wan-new", &net.UDPAddr{IP: net.IPv4zero, Port: 67},
			func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
				responseType := dhcpv4.MessageTypeOffer
				if packet.MessageType() == dhcpv4.MessageTypeRequest {
					if !packet.Options.Has(dhcpv4.OptionServerIdentifier) {
						return
					}
					responseType = dhcpv4.MessageTypeAck
				}
				reply, replyErr := dhcpv4.NewReplyFromRequest(packet,
					dhcpv4.WithMessageType(responseType),
					dhcpv4.WithYourIP(net.ParseIP("198.51.100.77")),
					dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.ParseIP("198.51.100.2"))),
					dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
					dhcpv4.WithOption(dhcpv4.OptRouter(net.ParseIP("198.51.100.3"))),
					dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(10*time.Minute)),
				)
				if replyErr == nil {
					_, _ = conn.WriteTo(reply.ToBytes(), peer)
				}
			})
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = server.Serve() }()
		defer func() { _ = server.Close() }()
		setRuntimeNamespace(t, gateway)
		waitStaticRuntimeAddress(t, daemon, "enatt0", "198.51.100.77/24", true)
	}

	waitDHCPRenamedLinkAddressRemoved(t, daemon, oldLink.Attrs().Index)
	assertRuntimeDaemonRunning(t, daemon)
	assertDHCPRenamedLinkForeignAddress(t, oldLink)
}

func waitDHCPRenamedLinkAddressRemoved(t *testing.T, daemon *runtimeDaemon, linkIndex int) {
	t.Helper()
	deadline := time.Now().Add(40 * time.Second)
	var addresses []netlink.Addr
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByIndex(linkIndex)
		if err != nil {
			t.Fatal(err)
		}
		addresses, err = netlink.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			t.Fatal(err)
		}
		present := false
		for _, address := range addresses {
			present = present || address.IPNet.String() == dhcpRenamedLinkAddress
		}
		if !present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("waitDHCPRenamedLinkAddressRemoved: addresses=%v: %s", addresses, runtimeLogTail(t, daemon, 30))
}
