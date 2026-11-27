//go:build linux && ovs

package commandline_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket/layers"
	"github.com/vishvananda/netlink"
)

func TestOVSRestartRecovery(t *testing.T) {
	host, err := os.Stat("/proc/1/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Stat("/proc/self/ns/net")
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(host, self) {
		t.Fatal("run make test-ovs to provide a private network namespace")
	}
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
	input, outputs, document := prepareOVSHealthNetwork(t)
	dir := t.TempDir()
	t.Setenv("OVS_RUNDIR", dir)
	socket := filepath.Join(dir, "db.sock")
	db := filepath.Join(dir, "conf.db")
	bridge := "unix:" + filepath.Join(dir, "br-hc.mgmt")
	path := filepath.Join(dir, "network.json")
	writeOVSPacketDocument(t, path, strings.ReplaceAll(document, "/run/openvswitch/", dir+"/"))
	run := func(name string, args ...string) string {
		t.Helper()
		output, err := exec.CommandContext(t.Context(), name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v: %s", name, args, err, output)
		}
		return string(output)
	}
	run("ovsdb-tool", "create", db, "/usr/share/openvswitch/vswitch.ovsschema")
	start := func() func() {
		stopDB := startOVSTestDaemon(t, dir, "ovsdb-server", "--remote=punix:"+socket, db)
		run("ovs-vsctl", "--retry", "--timeout=5", "--db=unix:"+socket, "--no-wait", "init")
		stopSwitch := startOVSTestDaemon(t, dir, "ovs-vswitchd", "unix:"+socket)
		return func() {
			stopSwitch()
			stopDB()
		}
	}
	stop := start()
	changeOVSBlockedProbe(t, "add", firstTarget)
	service := startOVSPacketService(t, path, "hc-in", 100*time.Millisecond)
	state := filepath.Join(filepath.Dir(service.config), "health.state")
	before := [2]bool{false, true}
	waitForOVSHealth(t, bridge, before)
	checkOVSProbeState(t, state, "unhealthy", "healthy")
	checkOVSHealthPackets(
		t,
		input,
		outputs,
		layers.EthernetTypeIPv4,
		22000,
		before,
		"before OVS restart",
	)

	// A flow outside the network document distinguishes restored rules from retained rules.
	const cookie = "cookie=0x4321"
	run(
		"ovs-ofctl",
		"-O",
		"OpenFlow15",
		"add-flow",
		bridge,
		cookie+",table=11,priority=10,actions=drop",
	)
	hasForeignFlow := func() bool {
		return strings.Contains(
			run("ovs-ofctl", "-O", "OpenFlow15", "dump-flows", bridge, cookie+"/-1"),
			cookie+",",
		)
	}
	if !hasForeignFlow() {
		t.Fatal("OVS did not install the independent flow before restart")
	}
	stop()
	changeOVSBlockedProbe(t, "add", secondTarget)
	changeOVSBlockedProbe(t, "delete", firstTarget)
	checkOVSProbeState(t, state, "healthy", "unhealthy")
	stop = start()
	after := [2]bool{true, false}
	waitForOVSHealth(t, bridge, after)
	if hasForeignFlow() {
		t.Fatal("OVS retained the independent flow after restart")
	}
	checkOVSHealthPackets(
		t,
		input,
		outputs,
		layers.EthernetTypeIPv4,
		23000,
		after,
		"after OVS restart",
	)
	checkOVSHealthPackets(
		t,
		input,
		outputs,
		layers.EthernetTypeIPv6,
		24000,
		after,
		"after OVS restart",
	)
	service.stop()
	stop()
}

func startOVSTestDaemon(t *testing.T, dir, name string, args ...string) func() {
	t.Helper()
	control := filepath.Join(dir, name+".ctl")
	cmd := exec.CommandContext(t.Context(), name, append(args, "--unixctl="+control)...)
	cmd.Stdout, cmd.Stderr = t.Output(), t.Output()
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	return func() {
		t.Helper()
		stop := exec.CommandContext(t.Context(), "ovs-appctl", "--timeout=5", "-t", control, "exit")
		if output, err := stop.CombinedOutput(); err != nil {
			t.Fatalf("stop %s: %v: %s", name, err, output)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("wait for %s: %v", name, err)
		}
	}
}
