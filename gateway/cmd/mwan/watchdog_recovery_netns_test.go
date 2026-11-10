//go:build linux && firewallnetns

package main

import (
	"context"
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

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	mwanv1 "goodkind.io/mwan/gen/mwan/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const watchdogRecoveryChild = "MWAN_WATCHDOG_RECOVERY_CHILD"

func TestWatchdogRecoveryProbesPrimaryGateway(t *testing.T) {
	if os.Getenv(watchdogRecoveryChild) != "1" {
		binary := protocolTestBinary(t)
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
		child.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags: uintptr(unix.CLONE_NEWNET | unix.CLONE_NEWNS),
		}
		child.Env = append(os.Environ(), watchdogRecoveryChild+"=1", forwardingBinaryEnv+"="+binary)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("watchdog recovery: %v: %s", err, output)
		}
		return
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	root := t.TempDir()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, name := range []string{
			"watchdog-run",
			"failover-agent",
			"failover-ifmgr",
			"primary-agent",
		} {
			content, err := os.ReadFile(filepath.Join(root, name+".log"))
			if err == nil {
				t.Logf("%s log: %s", name, content)
			}
		}
	})
	stateDir := filepath.Join(root, "mwan-state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	bindStartupDirectory(t, stateDir, "/var/lib/mwan")
	namespaces := newForwardingNamespaces(t)
	wireForwardingTopology(t, namespaces)
	startForwardingTargets(t, namespaces)
	startForwardingRouter(t)
	backupConfig := writeForwardingConfig(t, root, "failover", true)
	primaryConfig := writeForwardingConfig(t, root, "primary", false)
	startForwardingProcess(
		t,
		namespaces,
		namespaces.failover,
		root,
		"failover-ifmgr",
		backupConfig,
		"ifmgr",
		"--role",
		"failover",
	)
	startForwardingProcess(
		t,
		namespaces,
		namespaces.failover,
		root,
		"failover-agent",
		backupConfig,
		"agent",
		"--tcp-addr",
		fmt.Sprintf("%s:%d", forwardingFailoverNH4, forwardingAgentPort),
		"--vsock-port",
		"0",
	)
	startForwardingProcess(
		t,
		namespaces,
		namespaces.primary,
		root,
		"primary-agent",
		primaryConfig,
		"agent",
		"--tcp-addr",
		fmt.Sprintf("%s:%d", forwardingPrimaryNH4, forwardingAgentPort),
		"--vsock-port",
		"0",
	)
	waitRecoveryPrimaryAnnouncement(t, true, 60*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V4, forwardingFailoverNH4, 60*time.Second)
	waitForwardingDefault(t, netlink.FAMILY_V6, forwardingFailoverNH6, 60*time.Second)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.upstream,
		func() {
			setRecoveryPrimaryAddress(t, "198.51.100.6/30", false)
			setRecoveryPrimaryAddress(t, "2001:db8:f2::2/64", false)
			forwardingRoute(t, "192.0.2.8/30", "198.51.100.1")
			forwardingRoute(t, "2001:db8:10::/64", "2001:db8:f1::1")
		},
	)
	blackholes := []*netlink.Route{
		newRecoveryBlackhole(t, forwardingTarget4+"/32"),
		newRecoveryBlackhole(t, forwardingTarget6+"/128"),
	}
	for _, route := range blackholes {
		if err := netlink.RouteAdd(route); err != nil {
			t.Fatal(err)
		}
	}
	configPath := writeRecoveryWatchdogConfig(t, root)
	qm := filepath.Join(root, "qm")
	build := exec.Command("go", "build", "-o", qm, "../../internal/watchdog/testdata/qm")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build guest status helper: %v: %s", err, output)
	}
	logFile, err := os.Create(filepath.Join(root, "watchdog-run.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logFile.Close()
	command := exec.Command(os.Getenv(forwardingBinaryEnv), "watchdog", "--config", configPath)
	command.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"))
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	}()
	waitRecoveryPrimaryAnnouncement(t, false, 60*time.Second)
	for _, route := range blackholes {
		if err := netlink.RouteDel(route); err != nil {
			t.Fatal(err)
		}
	}
	waitRecoveryDeferred(t, filepath.Join(root, "watchdog.log"), 100*time.Second)
	waitRecoveryPrimaryAnnouncement(t, false, 30*time.Second)
	forwardingInNamespace(
		t,
		namespaces,
		namespaces.upstream,
		func() {
			setRecoveryPrimaryAddress(t, "198.51.100.6/30", true)
			setRecoveryPrimaryAddress(t, "2001:db8:f2::2/64", true)
		},
	)
	waitRecoveryPrimaryAnnouncement(t, true, 30*time.Second)
}

func setRecoveryPrimaryAddress(t *testing.T, cidr string, present bool) {
	t.Helper()
	link, err := netlink.LinkByName("fromprimary")
	if err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	address.Flags = unix.IFA_F_NODAD
	if present {
		err = netlink.AddrAdd(link, address)
	} else {
		err = netlink.AddrDel(link, address)
	}
	if err != nil {
		t.Fatalf("primary upstream address %s present=%t: %v", cidr, present, err)
	}
}

func newRecoveryBlackhole(t *testing.T, cidr string) *netlink.Route {
	t.Helper()
	_, destination, err := net.ParseCIDR(cidr)
	if err != nil {
		t.Fatal(err)
	}
	return &netlink.Route{Dst: destination, Table: unix.RT_TABLE_MAIN, Type: unix.RTN_BLACKHOLE}
}

func writeRecoveryWatchdogConfig(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "recovery.toml")
	config := fmt.Sprintf(
		"mwan_vmid = \"301\"\n[network]\nping_target_ipv4 = %q\nping_target_ipv6 = %q\n[watchdog]\nmwan_agent_tcp_addr = \"%s:%d\"\nconnectivity_timeout_seconds = 1\ncheck_interval_healthy_seconds = 1\ncheck_interval_degraded_seconds = 1\nlog_file = %q\njson_log_file = %q\nrollback_lock_file = %q\nrollback_state_file = %q\n[failover]\nlxc_id = \"203\"\nagent_tcp_addr = \"%s:%d\"\n",
		forwardingTarget4,
		forwardingTarget6,
		forwardingPrimaryNH4,
		forwardingAgentPort,
		filepath.Join(root, "watchdog.log"),
		filepath.Join(root, "watchdog.json"),
		filepath.Join(root, "rollback.lock"),
		filepath.Join(root, "rollback.state"),
		forwardingFailoverNH4,
		forwardingAgentPort,
	)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func recoveryPrimaryAnnouncement() (bool, error) {
	address := fmt.Sprintf("%s:%d", forwardingPrimaryNH4, forwardingAgentPort)
	connection, err := grpc.NewClient(
		address,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return false, err
	}
	defer connection.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := mwanv1.NewMWANAgentClient(connection).GetBGPStatus(
		ctx,
		&mwanv1.GetBGPStatusRequest{},
	)
	if err != nil {
		return false, err
	}
	return response.GetAnnouncing(), nil
}

func waitRecoveryPrimaryAnnouncement(t *testing.T, want bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastError error
	for time.Now().Before(deadline) {
		announcing, err := recoveryPrimaryAnnouncement()
		if err == nil && announcing == want {
			return
		}
		lastError = err
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("primary announcement did not become %t within %s: %v", want, timeout, lastError)
}

func waitRecoveryDeferred(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastError error
	for time.Now().Before(deadline) {
		announcing, err := recoveryPrimaryAnnouncement()
		if err != nil {
			lastError = err
			time.Sleep(100 * time.Millisecond)
			continue
		}
		if announcing {
			t.Fatal("primary announced routes before its upstream recovered")
		}
		data, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(data), "BGP_RECOVERY: deferred") {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("watchdog did not defer recovery within %s: %v", timeout, lastError)
}
