//go:build linux && systemd

package commandline_test

import (
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/internal/commandline"
)

func TestCLIConnectivityAbsentPrefixSystemd(t *testing.T) {
	commandline.RequireSystemd(t)
	if os.Getenv(debugProbeNamespaceChild) != t.Name() {
		ensureDebugNetworkd(t)
	}
	runDebugProbeIsolatedTest(t, runCLIConnectivityAbsentPrefix, uintptr(unix.CLONE_NEWNS))
}

func ensureDebugNetworkd(t *testing.T) {
	t.Helper()
	output, err := commandline.SystemdFixtureOutput(
		"systemctl",
		"show",
		"systemd-networkd.service",
		"--property=ActiveState",
		"--value",
	)
	if err != nil {
		t.Fatalf("read systemd-networkd state: %v: %s", err, output)
	}
	state := strings.TrimSpace(string(output))
	if state == "active" {
		return
	}
	if state != "inactive" {
		t.Fatalf("systemd-networkd state %q cannot be restored", state)
	}
	t.Cleanup(func() {
		if output, err := commandline.SystemdFixtureOutput(
			"systemctl",
			"stop",
			"systemd-networkd.service",
		); err != nil {
			t.Errorf("restore systemd-networkd state: %v: %s", err, output)
		}
	})
	if output, err := commandline.SystemdFixtureOutput(
		"systemctl",
		"start",
		"systemd-networkd.service",
	); err != nil {
		t.Fatalf("start systemd-networkd: %v: %s", err, output)
	}
}

func runCLIConnectivityAbsentPrefix(t *testing.T, binary string) {
	config := installDebugProbeNetwork(t)
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if loopback.Attrs().Flags&net.FlagUp == 0 {
		t.Fatal("loopback must be up in the isolated systemd container")
	}
	for _, cidr := range []string{"1.1.1.1/32", "2606:4700:4700::1111/128"} {
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			t.Fatal(err)
		}
		address.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(loopback, address); err != nil {
			t.Fatalf("add local probe target %s: %v", cidr, err)
		}
		t.Cleanup(func() {
			if err := netlink.AddrDel(loopback, address); err != nil {
				t.Errorf("remove local probe target %s: %v", cidr, err)
			}
		})
	}
	output, diagnostics, code := runCLIProcess(
		t,
		binary,
		"debug",
		"connectivity",
		"--config",
		config,
		"--output-format",
		"json",
	)
	var results []struct {
		Prefix        string `json:"prefix"`
		PrefixPresent bool   `json:"prefix_present"`
		Ping4Status   string `json:"ping4_status"`
		Ping6Status   string `json:"ping6_status"`
	}
	if code != 0 || json.Unmarshal([]byte(output), &results) != nil || len(results) != 1 ||
		results[0].Prefix != "" ||
		results[0].PrefixPresent ||
		results[0].Ping4Status != "OK" ||
		results[0].Ping6Status != "OK" {
		t.Fatalf("absent prefix exit=%d output=%s diagnostics=%s", code, output, diagnostics)
	}
	text, diagnostics, code := runCLIProcess(
		t,
		binary,
		"debug",
		"connectivity",
		"--config",
		config,
		"--output-format",
		"text",
	)
	if code != 0 || !strings.Contains(text, "  prefix: \n  prefix present: false\n") ||
		strings.Contains(text, "invalid Prefix") {
		t.Fatalf("absent prefix text exit=%d output=%s diagnostics=%s", code, text, diagnostics)
	}
}
