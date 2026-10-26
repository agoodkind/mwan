//go:build linux && netns

package npt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/agoodkind/netlink"
	"github.com/cilium/ebpf"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/addresses"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/nptpolicy"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	policyConnectionID = "policy"
	policyWANName      = "policywan"
	policyLANName      = "policylan"
	policyInternal     = "fd01:203:405::/48"
	policyExternal     = "2001:db8:1::/48"
)

type policyKernelFixture struct {
	module *Module
	store  *wanstate.Store
	wan    netlink.Link
	ctx    context.Context
}

func newPolicyKernelFixture(t *testing.T) policyKernelFixture {
	t.Helper()
	inIPv4Namespace(t)
	var wan, lan netlink.Link
	for _, name := range []string{policyWANName, policyLANName} {
		if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
			t.Fatal(err)
		}
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
		if name == policyWANName {
			wan = link
		} else {
			lan = link
		}
	}
	_, internal, err := net.ParseCIDR(policyInternal)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: lan.Attrs().Index, Dst: internal}); err != nil {
		t.Fatal(err)
	}
	translation := &config.IPv6Translation{Mode: config.TranslationNPTv6, NPT: &config.NPTv6Translation{
		InternalPrefix: netip.MustParsePrefix(policyInternal), ExternalSource: config.PrefixConfigured,
		ExternalPrefix: netip.MustParsePrefix(policyExternal),
	}}
	connection := interfaceintent.Connection{
		ID: policyConnectionID, Name: policyWANName, Owner: interfaceintent.OwnerMWAN,
		Roles: interfaceintent.RoleProvider, IPv6: &interfaceintent.IPv6{},
	}
	store := wanstate.New()
	env := &ifmgr.Env{Log: slog.Default(), LiveState: store, Connections: []interfaceintent.Connection{connection}, OwnedLinks: &ifmgr.OwnedLinkResults{}}
	env.OwnedLinks.Replace([]netif.OwnedLinkResult{{
		ConnectionID: policyConnectionID, Name: policyWANName, ActualName: policyWANName,
		IfIndex: wan.Attrs().Index, Status: netif.OwnedLinkReady,
	}})
	ctx, cancel := context.WithCancel(t.Context())
	authority, err := addresses.New(addresses.Config{
		Connections: env.Connections,
		Providers:   map[string]addresses.Provider{policyConnectionID: {IPv6: translation}},
		StateFile:   filepath.Join(t.TempDir(), "addresses.json"),
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := authority.Init(ctx, env); err != nil {
		cancel()
		t.Fatal(err)
	}
	if err := authority.Reconcile(ctx, env.Log); err != nil {
		cancel()
		t.Fatal(err)
	}
	created, err := New(Config{
		InternalIface: policyLANName, InternalPrefix: policyInternal,
		OpnsenseEdgeV6: "fd01:203:405::2", MwanbrEdgeV6: "fd01:203:405::3",
		WANs: []WAN{{WANRef: ifmgr.WANRef{ID: policyConnectionID, Name: policyConnectionID, Iface: policyWANName}, Owned: true, Translation: translation}},
	})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	module := created.(*Module)
	if err := module.Init(ctx, env); err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cancel()
		if err := module.translator.(*bpf.Translator).Close(); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(8 * time.Second)
	for {
		err := module.Reconcile(ctx, env.Log)
		if err == nil && store.Snapshot().Translation[policyConnectionID].V6.Ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("NPT baseline did not become ready: %v; %+v", err, store.Snapshot().Translation)
		}
		time.Sleep(25 * time.Millisecond)
	}
	return policyKernelFixture{module: module, store: store, wan: wan, ctx: ctx}
}

func policyMapBytes(t *testing.T, link netlink.Link) []byte {
	t.Helper()
	filters, err := netlink.FilterList(link, netlink.HANDLE_MIN_INGRESS)
	if err != nil {
		t.Fatal(err)
	}
	for _, filter := range filters {
		attached, ok := filter.(*netlink.BpfFilter)
		if !ok || attached.Name != "mwan-npt-ingress" {
			continue
		}
		program, err := ebpf.NewProgramFromID(ebpf.ProgramID(attached.Id))
		if err != nil {
			t.Fatal(err)
		}
		defer program.Close()
		information, err := program.Info()
		if err != nil {
			t.Fatal(err)
		}
		identities, available := information.MapIDs()
		if !available {
			t.Fatal("kernel did not expose the installed NPT policy map")
		}
		for _, identity := range identities {
			policyMap, err := ebpf.NewMapFromID(identity)
			if err != nil {
				t.Fatal(err)
			}
			info, err := policyMap.Info()
			if err != nil {
				policyMap.Close()
				t.Fatal(err)
			}
			if info.Name != "policies" {
				policyMap.Close()
				continue
			}
			value, lookupErr := policyMap.LookupBytes(uint32(link.Attrs().Index))
			closeErr := policyMap.Close()
			if lookupErr != nil || closeErr != nil {
				t.Fatalf("read installed policy: lookup=%v close=%v", lookupErr, closeErr)
			}
			return value
		}
	}
	t.Fatal("installed NPT policy map is absent")
	return nil
}

func addPolicyAddress(t *testing.T, link netlink.Link, address string) {
	t.Helper()
	value, err := netlink.ParseAddr(address + "/128")
	if err != nil {
		t.Fatal(err)
	}
	value.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(link, value); err != nil {
		t.Fatal(err)
	}
}

func TestAppliedNPTPolicySnapshotKernel(t *testing.T) {
	fixture := newPolicyKernelFixture(t)
	before := fixture.store.Snapshot()
	pair := before.NPTPolicies[policyConnectionID]
	if before.NPTPolicyGeneration == 0 || pair.ID == 0 || pair.Internal.String() != policyInternal || pair.External.String() != policyExternal ||
		!slices.Equal(pair.SourceExceptions, []netip.Addr{netip.MustParseAddr("fd01:203:405::2"), netip.MustParseAddr("fd01:203:405::3")}) ||
		!slices.Contains(pair.DestinationExceptions, netip.MustParseAddr("2001:db8:1::1")) {
		t.Fatalf("applied NPT snapshot differs from the kernel policy: %+v", before)
	}
	kernel := policyMapBytes(t, fixture.wan)
	modified := fixture.store.Snapshot()
	modified.NPTPolicies[policyConnectionID].SourceExceptions[0] = netip.MustParseAddr("fd00:dead::1")
	modified.NPTPolicies[policyConnectionID].DestinationExceptions[0] = netip.MustParseAddr("2001:db8:dead::1")
	delete(modified.NPTPolicies, policyConnectionID)
	actual := fixture.store.Snapshot().NPTPolicies[policyConnectionID]
	if !slices.Equal(actual.SourceExceptions, pair.SourceExceptions) || !slices.Equal(actual.DestinationExceptions, pair.DestinationExceptions) {
		t.Fatal("snapshot edits changed the stored exception slices")
	}
	reordered := fixture.store.Snapshot()
	pair = reordered.NPTPolicies[policyConnectionID]
	slices.Reverse(pair.SourceExceptions)
	pair.DestinationExceptions = append(pair.DestinationExceptions, pair.DestinationExceptions[0])
	reordered.NPTPolicies[policyConnectionID] = pair
	if fixture.store.SetNPTTranslation(reordered.Translation, reordered.NPTPolicies) || fixture.store.Snapshot().NPTPolicyGeneration != before.NPTPolicyGeneration {
		t.Fatal("exception ordering or duplication advanced the policy generation")
	}
	owner, cancel := context.WithCancel(t.Context())
	cancel()
	guard := wanstate.NPTGuard(func(context.Context, []string) (func(), error) {
		if len(fixture.store.Snapshot().NPTPolicies) == 0 {
			return nil, fmt.Errorf("guard owner cannot inspect the applied policy")
		}
		return nil, owner.Err()
	})
	unregister, err := fixture.store.RegisterNPTGuard(guard)
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	if duplicate, err := fixture.store.RegisterNPTGuard(guard); err == nil {
		duplicate()
		t.Fatal("a second NPT guard replaced its owner")
	}
	if err := fixture.module.Reconcile(fixture.ctx, slog.Default()); err != nil {
		t.Fatalf("unchanged policy invoked the rejecting guard: %v", err)
	}
	if fixture.store.Snapshot().NPTPolicyGeneration != before.NPTPolicyGeneration || !bytes.Equal(kernel, policyMapBytes(t, fixture.wan)) {
		t.Fatal("an unchanged reconciliation changed applied policy semantics")
	}
}

func TestNPTGuardRejectionPreservesKernelPolicy(t *testing.T) {
	fixture := newPolicyKernelFixture(t)
	owner, cancel := context.WithCancel(t.Context())
	cancel()
	unregister, err := fixture.store.RegisterNPTGuard(func(context.Context, []string) (func(), error) {
		if len(fixture.store.Snapshot().NPTPolicies) == 0 {
			return nil, fmt.Errorf("guard owner cannot inspect the applied policy")
		}
		return nil, owner.Err()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	before := fixture.store.Snapshot()
	kernel := policyMapBytes(t, fixture.wan)
	rules := runNFT(t, "list", "table", "ip6", "nat")
	addPolicyAddress(t, fixture.wan, "2001:db8:1::20")
	if err := fixture.module.Reconcile(fixture.ctx, slog.Default()); !errors.Is(err, context.Canceled) {
		t.Fatalf("closed guard owner did not reject the policy mutation: %v", err)
	}
	after := fixture.store.Snapshot()
	if after.NPTPolicyGeneration != before.NPTPolicyGeneration || !after.Translation[policyConnectionID].V6.Ready ||
		!nptpolicy.EqualPair(after.NPTPolicies[policyConnectionID], before.NPTPolicies[policyConnectionID]) ||
		!bytes.Equal(kernel, policyMapBytes(t, fixture.wan)) || rules != runNFT(t, "list", "table", "ip6", "nat") {
		t.Fatal("guard rejection changed the applied snapshot, BPF policy or nftables rules")
	}
}

func TestNPTPartialFailurePublishesBeforeGuardRelease(t *testing.T) {
	fixture := newPolicyKernelFixture(t)
	var admission sync.Mutex
	unregister, err := fixture.store.RegisterNPTGuard(func(context.Context, []string) (func(), error) {
		admission.Lock()
		return func() {
			state := fixture.store.Snapshot()
			_, applied := state.NPTPolicies[policyConnectionID]
			if state.Translation[policyConnectionID].V6.Ready || applied {
				t.Error("admission resumed before the uncertain NPT policy became unavailable")
			}
			admission.Unlock()
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	kernel := policyMapBytes(t, fixture.wan)
	rules := runNFT(t, "list", "table", "ip6", "nat")
	if err := fixture.module.translator.(*bpf.Translator).Close(); err != nil {
		t.Fatal(err)
	}
	addPolicyAddress(t, fixture.wan, "2001:db8:1::30")
	if err := fixture.module.Reconcile(fixture.ctx, slog.Default()); err == nil {
		t.Fatal("closed BPF descriptors unexpectedly reconciled")
	}
	if rules == runNFT(t, "list", "table", "ip6", "nat") {
		t.Fatal("the failure did not follow a real nftables update")
	}
	if !bytes.Equal(kernel, policyMapBytes(t, fixture.wan)) {
		t.Fatal("the failed BPF update changed its retained kernel policy")
	}
	admission.Lock()
	state := fixture.store.Snapshot()
	admission.Unlock()
	if state.Translation[policyConnectionID].V6.Ready || len(state.NPTPolicies) != 0 {
		t.Fatalf("partial failure preserved an admissible NPT policy: %+v", state)
	}
}

func TestNPTPreparedExceptionsPublishBeforeGuardRelease(t *testing.T) {
	fixture := newPolicyKernelFixture(t)
	address := netip.MustParseAddr("2001:db8:1::40")
	before := fixture.store.Snapshot()
	kernel := policyMapBytes(t, fixture.wan)
	var admission sync.Mutex
	unregister, err := fixture.store.RegisterNPTGuard(func(context.Context, []string) (func(), error) {
		admission.Lock()
		return func() {
			state := fixture.store.Snapshot()
			pair := state.NPTPolicies[policyConnectionID]
			if !state.Translation[policyConnectionID].V6.Ready || !slices.Contains(pair.DestinationExceptions, address) {
				t.Error("admission resumed before the applied local-address exception appeared")
			}
			admission.Unlock()
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	if err := fixture.module.Env.PrepareLocalIPv6(fixture.ctx, slog.Default(), policyWANName, []netip.Addr{address}); err != nil {
		t.Fatal(err)
	}
	after := fixture.store.Snapshot()
	if after.NPTPolicyGeneration <= before.NPTPolicyGeneration || bytes.Equal(kernel, policyMapBytes(t, fixture.wan)) {
		t.Fatal("local-address preparation did not publish the changed kernel policy")
	}
}

func TestNPTPreparedFailureWithdrawsPolicyBeforeRelease(t *testing.T) {
	fixture := newPolicyKernelFixture(t)
	var admission sync.Mutex
	unregister, err := fixture.store.RegisterNPTGuard(func(context.Context, []string) (func(), error) {
		admission.Lock()
		return func() {
			state := fixture.store.Snapshot()
			_, applied := state.NPTPolicies[policyConnectionID]
			if state.Translation[policyConnectionID].V6.Ready || applied {
				t.Error("failed local-address preparation released an admissible policy")
			}
			admission.Unlock()
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer unregister()
	kernel := policyMapBytes(t, fixture.wan)
	rules := runNFT(t, "list", "table", "ip6", "nat")
	if err := fixture.module.translator.(*bpf.Translator).Close(); err != nil {
		t.Fatal(err)
	}
	address := netip.MustParseAddr("2001:db8:1::50")
	if err := fixture.module.Env.PrepareLocalIPv6(fixture.ctx, slog.Default(), policyWANName, []netip.Addr{address}); err == nil {
		t.Fatal("closed BPF descriptors accepted local-address preparation")
	}
	if rules != runNFT(t, "list", "table", "ip6", "nat") || !bytes.Equal(kernel, policyMapBytes(t, fixture.wan)) {
		t.Fatal("failed local-address preparation changed the kernel policy")
	}
}

func TestNPTGuardUnregistrationWaitsForMutation(t *testing.T) {
	fixture := newPolicyKernelFixture(t)
	var admission sync.Mutex
	guard := wanstate.NPTGuard(func(context.Context, []string) (func(), error) {
		admission.Lock()
		return admission.Unlock, nil
	})
	unregister, err := fixture.store.RegisterNPTGuard(guard)
	if err != nil {
		t.Fatal(err)
	}
	desired := fixture.store.Snapshot().NPTPolicies
	pair := desired[policyConnectionID]
	pair.DestinationExceptions = append(pair.DestinationExceptions, netip.MustParseAddr("2001:db8:1::60"))
	desired[policyConnectionID] = pair
	_, finish, err := fixture.store.BeginNPTChange(t.Context(), desired)
	if err != nil {
		unregister()
		t.Fatal(err)
	}
	defer finish()
	started, removed := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		unregister()
		close(removed)
	}()
	<-started
	select {
	case <-removed:
		t.Fatal("NPT guard ownership changed during an active mutation")
	case <-time.After(25 * time.Millisecond):
	}
	finish()
	select {
	case <-removed:
	case <-time.After(time.Second):
		t.Fatal("NPT guard unregistration did not finish after mutation completion")
	}
	replacement, err := fixture.store.RegisterNPTGuard(guard)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement()
	unregister()
	if duplicate, err := fixture.store.RegisterNPTGuard(guard); err == nil {
		duplicate()
		t.Fatal("old unregistration removed a newer guard owner")
	}
}
