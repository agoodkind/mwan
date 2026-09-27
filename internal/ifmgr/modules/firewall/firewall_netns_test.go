//go:build linux && netns

package firewall

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/vishvananda/netns"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/wanstate"
)

func TestReconcileRepairsDeletedRulesAndRetriesFailedRefresh(t *testing.T) {
	runtime.LockOSThread()
	previous, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	current, err := netns.New()
	if err != nil {
		previous.Close()
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netns.Set(previous); err != nil {
			t.Error(err)
		}
		current.Close()
		previous.Close()
		runtime.UnlockOSThread()
	})

	policy := firewall.Config{
		Enabled:               true,
		InternalInterface:     "lan0",
		InternalNetworkIPv4:   netip.MustParsePrefix("192.0.2.0/24"),
		ManagementInterface:   "mgmt0",
		ManagementServices:    []firewall.Service{{Protocol: "tcp", Port: 22}},
		KnownInterfaces:       []string{"lan0", "mgmt0", "wan0"},
		Paths:                 []firewall.ForwardingPath{{InternalInterface: "lan0", ExternalInterface: "wan0", IPv4: true, IPv6: true}},
		Providers:             []firewall.Provider{{Interface: "wan0", Mark: 100, MasqueradeIPv4: true}},
		PinnedProvider:        "wan0",
		PinnedSourceIPv4:      netip.MustParseAddr("192.0.2.10"),
		PinnedSourceIPv6:      netip.MustParseAddr("2001:db8::10"),
		PinnedSourcePort:      51820,
		PinnedDestinationPort: 51821,
		PinnedSetV4Name:       "pinned_v4",
		PinnedSetV6Name:       "pinned_v6",
	}
	selected, err := New(policy)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := wanstate.New()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := selected.Init(ctx, &ifmgr.Env{Log: log, LiveState: store}); err != nil {
		t.Fatal(err)
	}
	assertRefreshFailure := func() {
		t.Helper()
		err := selected.Reconcile(ctx, log)
		if err == nil || !strings.Contains(err.Error(), "request destination refresh") {
			t.Fatalf("reconcile error = %v, want destination refresh failure", err)
		}
		state := store.Snapshot().IntendedByOwner["firewall"]
		if state.Rules == "" || !strings.Contains(state.Error, destinationRefreshService) {
			t.Fatalf("firewall state after refresh failure: %+v", state)
		}
	}
	assertRefreshFailure()
	runNFTTest(t, "list", "table", "inet", "filter")
	runNFTTest(t, "add", "element", "inet", "mangle", "pinned_v4", "{", "203.0.113.0/24", "}")
	assertRefreshFailure()
	setOutput := runNFTTest(t, "list", "set", "inet", "mangle", "pinned_v4")
	if !strings.Contains(setOutput, "203.0.113.0/24") {
		t.Fatalf("destination refresh was lost: %s", setOutput)
	}
	desired, err := firewall.Compile(policy)
	if err != nil {
		t.Fatal(err)
	}
	unchanged, err := firewall.ApplyWithReport(ctx, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(unchanged.CreatedSets) != 0 {
		t.Fatalf("existing sets reported as new: %+v", unchanged.CreatedSets)
	}
	runNFTTest(t, "flush", "ruleset")
	recreated, err := firewall.ApplyWithReport(ctx, desired)
	if err != nil {
		t.Fatal(err)
	}
	if len(recreated.CreatedSets) != 2 {
		t.Fatalf("recreated sets = %+v, want both destination sets", recreated.CreatedSets)
	}
	runNFTTest(t, "flush", "ruleset")
	assertRefreshFailure()
	for _, table := range []struct{ family, name string }{{"inet", "filter"}, {"ip", "nat"}, {"inet", "mangle"}} {
		runNFTTest(t, "list", "table", table.family, table.name)
	}
}

func runNFTTest(t *testing.T, args ...string) string {
	t.Helper()
	output, err := exec.Command("nft", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("nft %v: %v: %s", args, err, output)
	}
	return string(output)
}
