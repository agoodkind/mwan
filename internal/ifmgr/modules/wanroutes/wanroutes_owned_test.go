package wanroutes

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

// fakeLinkAddresses is an in-memory kernel address table behind the two netif
// address seams. Its write replaces whatever a link holds at the same address,
// which is what the kernel's address replace does, so a module that asked for
// the link address at /32 would visibly rewrite the link's own prefix here.
type fakeLinkAddresses struct {
	byIface map[string][]string
	listErr map[string]error
}

func (f *fakeLinkAddresses) list(
	_ context.Context, _ *slog.Logger, iface string,
) ([]netif.CurrentAddr, error) {
	if err := f.listErr[iface]; err != nil {
		return nil, err
	}
	held := make([]netif.CurrentAddr, 0, len(f.byIface[iface]))
	for _, cidr := range f.byIface[iface] {
		family := familyV4
		if strings.Contains(cidr, ":") {
			family = familyV6
		}
		held = append(held, netif.CurrentAddr{CIDR: cidr, Family: family, Flags: 0})
	}
	return held, nil
}

func (f *fakeLinkAddresses) reconcile(
	_ context.Context, _ *slog.Logger, iface string, desired []netif.AddrSpec,
) error {
	for _, spec := range desired {
		wanted := netip.MustParsePrefix(spec.CIDR)
		kept := make([]string, 0, len(f.byIface[iface])+1)
		for _, cidr := range f.byIface[iface] {
			if netip.MustParsePrefix(cidr).Addr() != wanted.Addr() {
				kept = append(kept, cidr)
			}
		}
		f.byIface[iface] = append(kept, spec.CIDR)
	}
	return nil
}

func addresses(values ...string) []netip.Addr {
	parsed := make([]netip.Addr, 0, len(values))
	for _, value := range values {
		parsed = append(parsed, netip.MustParseAddr(value))
	}
	return parsed
}

// newOwnershipModule gives att a routed block outside its lease's subnet and
// webpass an on-link block whose first mapped address is the link address, the
// two shapes the production providers have.
//
// Reconcile is driven through its public entry point, so the test must stop
// it before any real kernel write. The provider links do not exist in the test
// process, so gateway discovery fails right after ownership; the health state
// path sits under a regular file as a second stop, because opening it fails
// with a not-a-directory error rather than the missing-file case the reader
// treats as empty.
func newOwnershipModule(t *testing.T, kernel *fakeLinkAddresses, store *wanstate.Store) *Module {
	t.Helper()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("create blocker file: %v", err)
	}
	cfg := testConfig()
	cfg.HealthStateFile = filepath.Join(blocker, "mwan-health.state")
	cfg.WANs = append([]WAN(nil), cfg.WANs...)
	cfg.WANs[0].MappedExternals = addresses("198.51.100.193", "198.51.100.194")
	cfg.WANs[1].MappedExternals = addresses("203.0.113.2", "203.0.113.3", "203.0.113.4")
	module := &Module{cfg: cfg}
	module.InitBase(testEnvWithStore(store), "module", moduleName)
	module.listAddrs = kernel.list
	module.reconcileAddrs = kernel.reconcile
	return module
}

func sortedCopy(values []string) []string {
	copied := slices.Clone(values)
	slices.Sort(copied)
	return copied
}

func TestReconcileOwnsOnlyOnLinkAddresses(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	kernel := &fakeLinkAddresses{
		byIface: map[string][]string{
			"att0":     {"192.0.2.10/24"},
			"webpass0": {"203.0.113.2/29", "fe80::b/64"},
			"mbrains0": {"198.51.100.10/24"},
		},
		listErr: map[string]error{},
	}
	store := wanstate.New()
	module := newOwnershipModule(t, kernel, store)

	// Each pass ends with an error once ownership has run, because the rest of
	// the pass cannot read gateways or health here. The /32 writes must already
	// be in the kernel, and a second pass must find them and change nothing.
	for pass := 1; pass <= 2; pass++ {
		err := module.Reconcile(ctx, module.Log)
		if err == nil {
			t.Fatalf("pass %d: Reconcile returned nil, want the gateway or health read to stop the pass", pass)
		}
		if strings.Contains(err.Error(), "mapped addresses") || strings.Contains(err.Error(), "list addresses") {
			t.Fatalf("pass %d: ownership itself failed: %v", pass, err)
		}
	}
	module.publishLiveState(testGateways(), netif.HealthStates{})

	wantWebpass := []string{"203.0.113.2/29", "203.0.113.3/32", "203.0.113.4/32", "fe80::b/64"}
	if got := sortedCopy(kernel.byIface["webpass0"]); !reflect.DeepEqual(got, wantWebpass) {
		t.Fatalf("webpass0 addresses = %v, want %v", got, wantWebpass)
	}
	if got := kernel.byIface["att0"]; !reflect.DeepEqual(got, []string{"192.0.2.10/24"}) {
		t.Fatalf("att0 addresses = %v, want the lease alone", got)
	}
	routing := store.Snapshot().Routing
	if got := routing["webpass"].OwnedAddresses; !reflect.DeepEqual(got, addresses("203.0.113.3", "203.0.113.4")) {
		t.Fatalf("webpass owned addresses = %v, want 203.0.113.3 and 203.0.113.4", got)
	}
	if got := routing["att"].OwnedAddresses; len(got) != 0 {
		t.Fatalf("att owned addresses = %v, want none for a routed block", got)
	}
	if got := routing["monkeybrains"].OwnedAddresses; len(got) != 0 {
		t.Fatalf("monkeybrains owned addresses = %v, want none", got)
	}
}

func TestReconcileOwnsPastAnUnreadableLink(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	kernel := &fakeLinkAddresses{
		byIface: map[string][]string{
			"webpass0": {"203.0.113.2/29"},
		},
		listErr: map[string]error{"att0": errors.New("link not found")},
	}
	store := wanstate.New()
	module := newOwnershipModule(t, kernel, store)

	err := module.Reconcile(ctx, module.Log)
	if err == nil || !strings.Contains(err.Error(), "list addresses on att0") {
		t.Fatalf("Reconcile error = %v, want one naming the unreadable att0 link", err)
	}
	module.publishLiveState(testGateways(), netif.HealthStates{})

	if got := sortedCopy(kernel.byIface["webpass0"]); !reflect.DeepEqual(got, []string{"203.0.113.2/29", "203.0.113.3/32", "203.0.113.4/32"}) {
		t.Fatalf("webpass0 addresses = %v, want the link plus two /32s", got)
	}
	routing := store.Snapshot().Routing
	if got := routing["webpass"].OwnedAddresses; !reflect.DeepEqual(got, addresses("203.0.113.3", "203.0.113.4")) {
		t.Fatalf("webpass owned addresses = %v, want 203.0.113.3 and 203.0.113.4", got)
	}
	if got := routing["att"].OwnedAddresses; len(got) != 0 {
		t.Fatalf("att owned addresses = %v, want none when its link cannot be read", got)
	}
}
