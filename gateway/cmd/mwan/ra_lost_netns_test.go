//go:build linux && firewallnetns

package main

import (
	"bytes"
	"fmt"
	"net"
	"net/mail"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/ndp"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/testsmtp"
)

const (
	raLostChildEnv     = "MWAN_RA_LOST_CHILD"
	raLostBinaryEnv    = "MWAN_RA_LOST_BINARY"
	raLostAlertAfter   = 5 * time.Second
	raLostAlertSubject = "WARN: ra_lost: no RA observed within threshold"
	raLostRecovered    = "WARN: RECOVERED: ra_lost: RA observed again"
)

func requireRALostSubject(t *testing.T, server *testsmtp.Server, want string, wait time.Duration) {
	t.Helper()
	select {
	case data := <-server.Messages:
		message, err := mail.ReadMessage(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		if subject := message.Header.Get("Subject"); subject != want {
			t.Fatalf("Subject %q differs from expected subject %q.", subject, want)
		}
	case <-time.After(wait):
		t.Fatalf("The message with subject %q did not arrive.", want)
	}
}

func TestOOBRALostAlertsWithoutAnyRouterAdvertisementAndRecovers(t *testing.T) {
	if os.Getenv(raLostChildEnv) != "1" {
		binary := protocolTestBinary(t)
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS)}
		child.Env = append(os.Environ(), raLostChildEnv+"=1", raLostBinaryEnv+"="+binary)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("Child test process %s failed: %v. Output: %s", t.Name(), err, output)
		}
		return
	}
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
	server := testsmtp.Start(t)
	downstream := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29"}, []string{"192.0.2.2/29"}, "")
	defer downstream.namespace.Close()
	provider := newRuntimePeer(t, gateway, "enatt0", "wan-vlan", nil, []string{"198.51.100.2/24"}, "")
	defer provider.namespace.Close()
	leaseDirectory := filepath.Join(root, "leases")
	if err := os.Mkdir(leaseDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.toml")
	config := fmt.Sprintf("[email]\nalert_email = \"recipient@example.com\"\nmsmtprc_path = %q\nmin_level = \"WARN\"\n[ifmgr]\nrole = \"oob\"\nreconcile_interval = \"1s\"\nlease_directory = %q\n[ifmgr.iface.enatt0]\ndhcp_v4 = true\n[ifmgr.modules.oobv6]\niface = \"enatt0\"\noob_addr = \"2001:db8::100/128\"\noob_table_id = 100\nmanage_slaac_source_rule = false\n[ifmgr.modules.oobv4]\niface = \"enatt0\"\noob_table_id = 100\n[ifmgr.modules.ra_lost]\niface = \"enatt0\"\nra_lost_alert_after = %q\n[[ifmgr.modules.policy_rules.rule]]\nfamily = \"ipv4\"\npriority = 100\nfrom = \"192.0.2.2/32\"\ntable_id = 100\n", server.Path, leaseDirectory, raLostAlertAfter.String())
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now()
	daemon := startRoleDHCPv4RuntimeDaemon(t, os.Getenv(raLostBinaryEnv), configPath, root, "oob")
	defer func() { stopRuntimeDaemon(t, daemon) }()
	requireRALostSubject(t, server, raLostAlertSubject, 30*time.Second)
	if elapsed := time.Since(startedAt); elapsed < raLostAlertAfter {
		t.Fatalf("The alert arrived %s after the time recorded before daemon startup, earlier than the %s threshold.", elapsed, raLostAlertAfter)
	}
	setRuntimeNamespace(t, provider.namespace)
	device, err := net.InterfaceByName("wan-vlan")
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := ndp.Listen(device, ndp.LinkLocal)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	advertisement := &ndp.RouterAdvertisement{RouterLifetime: 30 * time.Minute}
	if err := conn.WriteTo(advertisement, nil, netip.MustParseAddr("ff02::1")); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, gateway)
	requireRALostSubject(t, server, raLostRecovered, 30*time.Second)
}
