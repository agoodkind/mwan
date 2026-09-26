//go:build linux && netns

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestDeployGateEgressNetNS(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	requester, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer requester.Close()
	responder, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer responder.Close()
	defer func() {
		if err := netns.Set(original); err != nil {
			t.Error(err)
		}
	}()

	setupEgressResponder(t, requester)
	setTestNamespace(t, requester)
	setupEgressRequester(t)
	setTestNamespace(t, original)

	code, output := runEgressCLI(t, requester, original, "check-egress", "ipv4,ipv6")
	if code != 0 || !strings.Contains(output, "ipv6=yes ipv4=yes") {
		t.Fatalf("healthy check: code=%d output=%s", code, output)
	}
	for _, args := range [][]string{
		{"check-egress"},
		{"check-egress", "ipv4,ipv4"},
		{"wait-egress", "4", "ipv4,ipv6", "0"},
	} {
		code, output = runEgressCLI(t, requester, original, args...)
		if code != exitDeployGateUsage {
			t.Fatalf("invalid configuration %v: code=%d output=%s", args, code, output)
		}
	}

	setIPv4EchoDrop(t, responder, original, true)
	code, output = runEgressCLI(t, requester, original, "check-egress", "ipv4,ipv6")
	if code != exitDeployGateFailed || !strings.Contains(output, "ipv6=yes ipv4=no") {
		t.Fatalf("IPv4 failure check: code=%d output=%s", code, output)
	}
	code, output = runEgressCLI(t, requester, original, "check-egress", "ipv6")
	if code != 0 || !strings.Contains(output, "ipv4=not-required") {
		t.Fatalf("IPv6-only check: code=%d output=%s", code, output)
	}
	code, output = runEgressCLI(t, requester, original, "wait-egress", "4", "ipv4,ipv6", "3")
	if code != exitDeployGateFailed || !strings.Contains(output, "consecutive=0/3") {
		t.Fatalf("IPv4 failure wait: code=%d output=%s", code, output)
	}
	setIPv4EchoDrop(t, responder, original, false)
	code, output = runEgressCLI(t, requester, original, "wait-egress", "8", "ipv4,ipv6", "3")
	if code != 0 || !strings.Contains(output, "consecutive=3/3") {
		t.Fatalf("recovered wait: code=%d output=%s", code, output)
	}

	// Change the responder after the first successful round. The gate must
	// count three new complete rounds after the failed round.
	command := exec.Command(os.Args[0], "deploy-gate", "wait-egress", "12", "ipv4,ipv6", "3")
	command.Env = append(os.Environ(), childMainEnv+"=1")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	setTestNamespace(t, requester)
	startErr := command.Start()
	setTestNamespace(t, original)
	if startErr != nil {
		t.Fatal(startErr)
	}
	scanner := bufio.NewScanner(stdout)
	var lines []string
	blocked := false
	restored := false
	for scanner.Scan() {
		line := scanner.Text()
		lines = append(lines, line)
		if !blocked && strings.Contains(line, "consecutive=1/3") {
			setIPv4EchoDrop(t, responder, original, true)
			blocked = true
			continue
		}
		if blocked && !restored && strings.Contains(line, "consecutive=0/3") {
			setIPv4EchoDrop(t, responder, original, false)
			restored = true
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("recovery command: %v\nstdout:\n%s\nstderr:\n%s", err,
			strings.Join(lines, "\n"), stderr.String())
	}
	if !blocked || !restored || !strings.Contains(strings.Join(lines, "\n"), "consecutive=3/3") {
		t.Fatalf("failed round did not reset the count: %s", strings.Join(lines, "\n"))
	}
}

func setupEgressResponder(t *testing.T, requester netns.NsHandle) {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: "egress-server"}, PeerName: "egress-client",
	}); err != nil {
		t.Fatal(err)
	}
	server, err := netlink.LinkByName("egress-server")
	if err != nil {
		t.Fatal(err)
	}
	client, err := netlink.LinkByName("egress-client")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(client, int(requester)); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{
		"10.77.0.1/30", "fd77::1/64",
		deployGateEgressTargetV4 + "/32", deployGateEgressTargetV6 + "/128",
	} {
		addEgressAddress(t, server, address)
	}
	if err := netlink.LinkSetUp(server); err != nil {
		t.Fatal(err)
	}
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
}

func setupEgressRequester(t *testing.T) {
	t.Helper()
	client, err := netlink.LinkByName("egress-client")
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"10.77.0.2/30", "fd77::2/64"} {
		addEgressAddress(t, client, address)
	}
	if err := netlink.LinkSetUp(client); err != nil {
		t.Fatal(err)
	}
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
	for _, route := range []struct{ target, gateway string }{
		{deployGateEgressTargetV4 + "/32", "10.77.0.1"},
		{deployGateEgressTargetV6 + "/128", "fd77::1"},
	} {
		_, destination, err := net.ParseCIDR(route.target)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.RouteAdd(&netlink.Route{
			LinkIndex: client.Attrs().Index, Dst: destination, Gw: net.ParseIP(route.gateway),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func addEgressAddress(t *testing.T, link netlink.Link, address string) {
	t.Helper()
	parsed, err := netlink.ParseAddr(address)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.IP.To4() == nil {
		parsed.Flags = unix.IFA_F_NODAD
	}
	if err := netlink.AddrAdd(link, parsed); err != nil {
		t.Fatal(err)
	}
}

func setTestNamespace(t *testing.T, namespace netns.NsHandle) {
	t.Helper()
	if err := netns.Set(namespace); err != nil {
		t.Fatal(err)
	}
}

func setIPv4EchoDrop(t *testing.T, responder, original netns.NsHandle, drop bool) {
	t.Helper()
	setTestNamespace(t, responder)
	var commands [][]string
	if drop {
		commands = [][]string{
			{"add", "table", "ip", "mwan_egress_test"},
			{"add", "chain", "ip", "mwan_egress_test", "input", "{ type filter hook input priority 0; policy accept; }"},
			{"add", "rule", "ip", "mwan_egress_test", "input", "ip", "protocol", "icmp", "icmp", "type", "echo-request", "drop"},
		}
	} else {
		commands = [][]string{{"delete", "table", "ip", "mwan_egress_test"}}
	}
	for _, arguments := range commands {
		if output, err := exec.Command("nft", arguments...).CombinedOutput(); err != nil {
			setTestNamespace(t, original)
			t.Fatalf("nft %v: %v: %s", arguments, err, output)
		}
	}
	setTestNamespace(t, original)
}

func runEgressCLI(t *testing.T, requester, original netns.NsHandle, args ...string) (int, string) {
	t.Helper()
	arguments := append([]string{"deploy-gate"}, args...)
	command := exec.Command(os.Args[0], arguments...)
	command.Env = append(os.Environ(), childMainEnv+"=1")
	setTestNamespace(t, requester)
	output, err := command.CombinedOutput()
	setTestNamespace(t, original)
	if err == nil {
		return exitDeployGateOK, string(output)
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), string(output)
	}
	t.Fatal(fmt.Errorf("run egress command: %w: %s", err, output))
	return 0, ""
}
