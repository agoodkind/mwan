//go:build linux && firewallnetns

package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cilium/ebpf"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/addresses"
	"goodkind.io/mwan/internal/interfaceintent"
)

func checkRuntimeInternalNPTRelocation(t *testing.T, gateway netns.NsHandle, configPath, networkDir, root string) {
	t.Helper()
	configuration, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(strings.ReplaceAll(string(configuration), `reconcile_interval = "100ms"`, `reconcile_interval = "1h"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	writeRuntimeLegacyNPT(t, networkDir, "2001:db8:beef:900::/60", false)
	original := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "mapped-original-internal")
	defer killOwnedRuntimeDaemon(t, original)
	waitMappedRuntimeRule(t, original, "ip6", "nat", "2001:db8:beef:900::1", 10*time.Second)
	waitRuntimeAttachedEdge(t, "enmwanbr0", "2001:db8:beef:900::1")
	killOwnedRuntimeDaemon(t, original)
	prepareRuntimeRelocationEdge(t, original, root)
	former, err := netlink.LinkByName("enmwanbr0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetName(former, "former-lan"); err != nil {
		t.Fatal(err)
	}
	replacement := newRuntimePeer(t, gateway, "enmwanbr0", "replacement-lan", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, []string{"192.0.2.3/29", "2001:db8:b01:fe::2/64"}, "")
	defer replacement.namespace.Close()
	setRuntimeNamespace(t, gateway)
	link, err := netlink.LinkByName("enmwanbr0")
	if err != nil {
		t.Fatal(err)
	}
	_, internalPrefix, err := net.ParseCIDR("2001:db8:b01::/60")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: internalPrefix}); err != nil {
		t.Fatal(err)
	}
	if err := netlink.QdiscAdd(&netlink.Clsact{QdiscAttrs: netlink.QdiscAttrs{LinkIndex: link.Attrs().Index, Handle: netlink.MakeHandle(0xffff, 0), Parent: netlink.HANDLE_CLSACT}}); err != nil {
		t.Fatal(err)
	}
	conflict := &netlink.MatchAll{FilterAttrs: netlink.FilterAttrs{LinkIndex: link.Attrs().Index, Parent: netlink.HANDLE_MIN_EGRESS, Handle: 1, Priority: 45010, Protocol: unix.ETH_P_ALL}}
	if err := netlink.FilterAdd(conflict); err != nil {
		t.Fatal(err)
	}
	writeRuntimeLegacyNPT(t, networkDir, "2001:db8:beef:a00::/60", false)
	current := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "mapped-replacement-internal")
	defer killOwnedRuntimeDaemon(t, current)
	defer func() {
		if t.Failed() {
			t.Logf("complete replacement internal daemon log: %s", runtimeDaemonLog(t, current))
		}
	}()
	waitMappedRuntimeRule(t, current, "ip6", "nat", "2001:db8:beef:a00::1", 10*time.Second)
	waitStaticRuntimeLog(t, current, "priority 45010 is already used")
	waitRuntimeAttachedEdge(t, "former-lan", "2001:db8:beef:900::1")
	time.Sleep(500 * time.Millisecond)
	waitStaticRuntimeAddress(t, current, "enwebpass0", "2001:db8:beef:900::1/128", true)
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"), "2001:db8:beef:900::1/128", "2001:db8:beef:a00::1/128")
	if err := netlink.FilterDel(conflict); err != nil {
		t.Fatal(err)
	}
	provider, err := netlink.LinkByName("enwebpass0")
	if err != nil {
		t.Fatal(err)
	}
	eventAddress, err := netlink.ParseAddr("fd20::100/128")
	if err != nil {
		t.Fatal(err)
	}
	// NPT reconciles after actual WAN address events, not TC filter changes.
	if err := netlink.AddrAdd(provider, eventAddress); err != nil {
		t.Fatal(err)
	}
	waitStaticRuntimeAddress(t, current, "enwebpass0", "2001:db8:beef:900::1/128", false)
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"), "2001:db8:beef:a00::1/128")
}

func prepareRuntimeRelocationEdge(t *testing.T, original *runtimeDaemon, root string) {
	t.Helper()
	authority, err := addresses.New(addresses.Config{
		Connections: []interfaceintent.Connection{{ID: "webpass", Name: "enwebpass0", Owner: interfaceintent.OwnerExternal, Roles: interfaceintent.RoleProvider}},
		Providers: map[string]addresses.Provider{"webpass": {IPv6: &config.IPv6Translation{
			Mode: config.TranslationNPTv6,
			NPT:  &config.NPTv6Translation{InternalPrefix: netip.MustParsePrefix("2001:db8:b01::/60"), ExternalSource: config.PrefixConfigured, ExternalPrefix: netip.MustParsePrefix("2001:db8:beef:a00::/60")},
		}}},
		StateFile: filepath.Join(root, "owned-addresses.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	env := &ifmgr.Env{Log: slog.Default()}
	if err := authority.Init(ctx, env); err != nil {
		t.Fatal(err)
	}
	request := ifmgr.NPTEdgeRequest{ConnectionID: "webpass", Interface: "enwebpass0", Prefix: netip.MustParsePrefix("2001:db8:beef:a00::1/128")}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := env.NPTAddresses.Ensure(ctx, slog.Default(), request); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("replacement NPT edge did not complete DAD; original daemon log: %s", runtimeDaemonLog(t, original))
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func waitRuntimeAttachedEdge(t *testing.T, interfaceName, edge string) {
	t.Helper()
	link, err := netlink.LinkByName(interfaceName)
	if err != nil {
		t.Fatal(err)
	}
	address := netip.MustParseAddr(edge).As16()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		matched := 0
		for _, parent := range []uint32{netlink.HANDLE_MIN_INGRESS, netlink.HANDLE_MIN_EGRESS} {
			filters, err := netlink.FilterList(link, parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, filter := range filters {
				attached, ok := filter.(*netlink.BpfFilter)
				if !ok || attached.Handle != 0x4e50 || attached.Priority != 45010 || (attached.Name != "mwan-npt-ingress" && attached.Name != "mwan-npt-egress") {
					continue
				}
				if runtimeProgramReferencesEdge(t, attached.Id, uint32(link.Attrs().Index), address) {
					matched++
				}
			}
		}
		if matched == 2 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s does not have both attached NPT programs referencing edge %s", interfaceName, edge)
}

func runtimeProgramReferencesEdge(t *testing.T, programID int, index uint32, address [16]byte) bool {
	t.Helper()
	program, err := ebpf.NewProgramFromID(ebpf.ProgramID(programID))
	if err != nil {
		t.Fatal(err)
	}
	defer program.Close()
	info, err := program.Info()
	if err != nil {
		t.Fatal(err)
	}
	mapIDs, available := info.MapIDs()
	if !available {
		t.Fatal("attached NPT program map identities are unavailable")
	}
	for _, mapID := range mapIDs {
		policyMap, err := ebpf.NewMapFromID(mapID)
		if err != nil {
			t.Fatal(err)
		}
		mapInfo, err := policyMap.Info()
		if err != nil {
			policyMap.Close()
			t.Fatal(err)
		}
		if mapInfo.Name != "policies" {
			policyMap.Close()
			continue
		}
		value, err := policyMap.LookupBytes(index)
		policyMap.Close()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(value, address[:]) {
			t.Logf("interface %d attached program %d references policy map %d with edge %s", index, programID, mapID, netip.AddrFrom16(address))
			return true
		}
	}
	return false
}
