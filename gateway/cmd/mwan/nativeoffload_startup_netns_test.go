//go:build linux && cgo && nativeoffload && firewallnetns

package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/agoodkind/netlink"
	"github.com/google/nftables"
)

func TestNativeStartupPreservesLinuxReadiness(t *testing.T) {
	testNativeStartupReadiness(t, false)
}

func TestNativeStartupRetainsOwnership(t *testing.T) {
	testNativeStartupReadiness(t, true)
}

func testNativeStartupReadiness(t *testing.T, retained bool) {
	t.Helper()
	runBGPRuntimeIsolated(t, func(t *testing.T, run *bgpRuntimeRun) {
		provider := bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeRemoteASN, "::/0")
		run.topology = buildBGPRuntimeTopology(t, run.gateway, provider)
		writeBGPRuntimeNetwork(t, run.networkDir, provider)
		configureNativeStartupRuntime(t, run, retained)
		daemon := run.start(t, "native-startup")
		waitRuntimeTable(t, daemon, "inet", "mwan_steer", 10*time.Second)
		waitStaticRuntimeLog(t, daemon, "native automatic admission stopped")
		waitStaticRuntimeLog(t, daemon, "steering: nft monitor established")
		if retained {
			router := run.startRouter(t, provider)
			router.announce(t, provider.learned, provider.peer())
			waitBGPRuntimeSession(t, daemon, run.read, provider.device, "an established peer", func(session bgpRuntimeSession) bool {
				return session.Established
			})
			waitNativeStartupReadiness(t, daemon, run.read, tunnelRuntimeUnderlay, "ipv4", "not-ready")
			waitNativeStartupReadiness(t, daemon, run.read, provider.device, "ipv6", "not-ready")
			waitBGPRuntimeReceived(t, daemon, run.read, router)
			before := len(runtimeDaemonLog(t, daemon))
			runRuntimeNFT(t, "delete", "table", "inet", "mwan_steer")
			waitNativeStartupMutationBlocked(t, daemon, before)
			tables, err := (&nftables.Conn{}).ListTables()
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range tables {
				if table.Family == nftables.TableFamilyINet && table.Name == "mwan_steer" {
					t.Fatal("uncertain native startup allowed steering repair")
				}
			}
			return
		}
		runRuntimeNFT(t, "delete", "table", "inet", "mwan_steer")
		waitStaticRuntimeLog(t, daemon, "native operation 1")
		waitRuntimeTable(t, daemon, "inet", "mwan_steer", 10*time.Second)
		router := run.startRouter(t, provider)
		router.announce(t, provider.learned, provider.peer())
		waitNativeStartupReadiness(t, daemon, run.read, tunnelRuntimeUnderlay, "ipv4", "ready")
		waitTunnelRuntimeFamily(t, daemon, run.read, provider.device, "ipv6", "ready", "ready", tunnelRuntimeRecoverWindow)
		waitBGPRuntimeReceived(t, daemon, run.read, router, netip.MustParsePrefix(bgpRuntimeHome))
		requireBGPRuntimeExchange(t, run, daemon, provider)
	})
}

func waitNativeStartupReadiness(t *testing.T, daemon *runtimeDaemon, read func() string, name, family, readiness string) {
	t.Helper()
	waitBGPRuntime(t, daemon, read, name+" "+family+" readiness "+readiness, tunnelRuntimeRecoverWindow, func() bool {
		entry, found := readTunnelRuntimeInterface(read, name)
		return found && entry.family(family).Ownership.Readiness == readiness
	})
}

func configureNativeStartupRuntime(t *testing.T, run *bgpRuntimeRun, retained bool) {
	t.Helper()
	link, err := netlink.LinkByName(tunnelRuntimeUnderlay)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(run.networkDir, "network.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	interfaces := document["ietf-interfaces:interfaces"]
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var name string
		if err := json.Unmarshal(entry["name"], &name); err != nil {
			t.Fatal(err)
		}
		if name == tunnelRuntimeUnderlay {
			entry["goodkind-mwan-steering:link"] = json.RawMessage(fmt.Sprintf(`{"match":{"hardware-address":%q},"native-offload":{"data-pci":"ffff:ff:1f.5","data-mac":"02:00:00:00:00:03","controller":{"pci":"ffff:ff:1f.6","receiver-vf":1,"data-vf":2,"physical-miss":true}}}`, link.Attrs().HardwareAddr.String()))
		}
	}
	interfaces["interface"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	process, err := os.ReadFile(run.configPath)
	if err != nil {
		t.Fatal(err)
	}
	cpu, mbufs := uint32(0), uint32(1)
	if retained {
		cpu, mbufs = ^uint32(0), 8192
	}
	process = fmt.Appendf(process, "\n[ifmgr.modules.nativeoffload]\nenabled = true\ncpu = %d\nmemory_mib = 64\nhugepage_directory = %q\nfile_prefix = %q\nmbuf_count = %d\n", cpu, run.root, fmt.Sprintf("native-startup-%d", os.Getpid()), mbufs)
	if err := os.WriteFile(run.configPath, process, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitNativeStartupMutationBlocked(t *testing.T, daemon *runtimeDaemon, before int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		output := runtimeDaemonLog(t, daemon)
		if len(output) >= before && strings.Contains(output[before:], "prepare steering forwarding change: prepare forwarding mutation: native startup retains resources") {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("steering repair did not report retained native ownership: %s", runtimeLogTail(t, daemon, 40))
}
