//go:build linux && netns

package wanroutes

import (
	"context"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

// enterPrivateNetworkNamespace locks the test goroutine to its OS thread and
// moves that thread into a new, empty network namespace, so the links, routes
// and rules the test creates never reach the host. The thread is never unlocked
// because a goroutine that exits while locked takes its thread with it, so no
// other test ever runs in the namespace. A missing privilege fails the test
// rather than skipping it.
func enterPrivateNetworkNamespace(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Fatalf("unshare a network namespace (needs CAP_SYS_ADMIN, and CAP_NET_ADMIN for the links): %v", err)
	}
}

// providerLink is one dummy provider link with its addresses and the default
// gateways the main table routes through it. Each provider's main-table
// defaults carry their own metric, because two defaults with the same metric
// share one route slot and the second write replaces the first.
type providerLink struct {
	iface     string
	addresses []string
	gatewayV4 string
	gatewayV6 string
	metric    int
}

// presentProviders are AT&T's and Webpass's links from testConfig. Monkeybrains'
// link, mbrains0, is never created.
var presentProviders = []providerLink{
	{
		iface:     "att0",
		addresses: []string{"192.0.2.10/24", "2001:db8:1::10/64"},
		gatewayV4: "192.0.2.1",
		gatewayV6: "2001:db8:1::1",
		metric:    100,
	},
	{
		iface:     "webpass0",
		addresses: []string{"203.0.113.2/29", "2001:db8:2::10/64"},
		gatewayV4: "203.0.113.1",
		gatewayV6: "2001:db8:2::1",
		metric:    200,
	},
}

func addDummyLink(t *testing.T, iface string) {
	t.Helper()
	attributes := netlink.NewLinkAttrs()
	attributes.Name = iface
	link := &netlink.Dummy{LinkAttrs: attributes}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("add dummy link %s: %v", iface, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatalf("set %s up: %v", iface, err)
	}
}

// buildProviderLinks creates the internal link and the present provider links,
// and gives each provider link its addresses and main-table default routes, the
// state a provider's DHCP or static configuration leaves behind.
func buildProviderLinks(ctx context.Context, t *testing.T, module *Module) {
	t.Helper()
	addDummyLink(t, module.cfg.InternalIface)
	for _, provider := range presentProviders {
		addDummyLink(t, provider.iface)
		specs := make([]netif.AddrSpec, 0, len(provider.addresses))
		for _, address := range provider.addresses {
			specs = append(specs, netif.AddrSpec{CIDR: address, Family: ""})
		}
		if err := netif.ReconcileAddrs(ctx, module.Log, provider.iface, specs); err != nil {
			t.Fatalf("address %s: %v", provider.iface, err)
		}
		gateways := []struct{ family, via string }{
			{familyV4, provider.gatewayV4},
			{familyV6, provider.gatewayV6},
		}
		for _, gateway := range gateways {
			mainDefault := netif.RouteSpec{
				Family:   gateway.family,
				Dest:     "default",
				Via:      gateway.via,
				Dev:      provider.iface,
				TableID:  unix.RT_TABLE_MAIN,
				Metric:   provider.metric,
				Protocol: 0,
			}
			if err := netif.ReconcileTableDefault(ctx, module.Log, mainDefault); err != nil {
				t.Fatalf("main default %s via %s: %v", provider.iface, gateway.via, err)
			}
		}
	}
}

// newNamespaceModule wires a module whose every kernel read and write is the
// real netif implementation. The health file path does not exist, which the
// reader treats as no verdict recorded, so every provider reads healthy.
func newNamespaceModule(t *testing.T, store *wanstate.Store) *Module {
	t.Helper()
	cfg := testConfig()
	cfg.HealthStateFile = filepath.Join(t.TempDir(), "mwan-health.state")
	module := &Module{cfg: cfg}
	module.InitBase(testEnvWithStore(store), "module", moduleName)
	module.resolveNextHop = netif.NextHopResolves
	module.listAddrs = netif.ListAddrs
	module.reconcileAddrs = netif.ReconcileAddrs
	return module
}

type installedRule struct {
	family   string
	priority int
	tableID  int
}

// providerRules reads the kernel's rules that point at a provider table.
func providerRules(ctx context.Context, t *testing.T, module *Module) map[installedRule]bool {
	t.Helper()
	providerTables := map[int]bool{}
	for _, wan := range module.cfg.WANs {
		providerTables[wan.TableID] = true
	}
	installed := map[installedRule]bool{}
	for _, family := range []string{familyV4, familyV6} {
		rules, err := netif.ListRules(ctx, module.Log, family)
		if err != nil {
			t.Fatalf("list %s rules: %v", family, err)
		}
		for _, rule := range rules {
			if providerTables[rule.TableID] {
				installed[installedRule{family: family, priority: rule.Priority, tableID: rule.TableID}] = true
			}
		}
	}
	return installed
}

func expectedRules(rules []netif.DesiredRule, tableIDs ...int) map[installedRule]bool {
	expected := map[installedRule]bool{}
	for _, rule := range rules {
		for _, tableID := range tableIDs {
			if rule.TableID == tableID {
				expected[installedRule{family: rule.Family, priority: rule.Priority, tableID: tableID}] = true
			}
		}
	}
	return expected
}

// tableGateways reads the gateway of every routed entry in one provider table.
func tableGateways(ctx context.Context, t *testing.T, module *Module, tableID int) []string {
	t.Helper()
	var gateways []string
	for _, family := range []string{familyV4, familyV6} {
		routes, err := netif.ListTableRoutes(ctx, module.Log, family, tableID)
		if err != nil {
			t.Fatalf("list table %d %s routes: %v", tableID, family, err)
		}
		for _, route := range routes {
			if route.Via != "" {
				gateways = append(gateways, route.Via+" dev "+route.Dev)
			}
		}
	}
	return gateways
}

// reconcileWithMonkeybrainsMissing runs one pass and checks everything a pass
// with the Monkeybrains link absent must leave in the kernel: AT&T's and
// Webpass's rules and table defaults, no Monkeybrains rule or default, and an
// error that names only Monkeybrains.
func reconcileWithMonkeybrainsMissing(ctx context.Context, t *testing.T, module *Module, store *wanstate.Store) {
	t.Helper()
	err := module.Reconcile(ctx, module.Log)
	if err == nil {
		t.Fatal("Reconcile returned nil, want an error naming the missing monkeybrains link")
	}
	errorLines := strings.Split(err.Error(), "\n")
	wantLines := []string{
		`monkeybrains inet default gateway: link "mbrains0": Link not found`,
		`monkeybrains inet6 default gateway: link "mbrains0": Link not found`,
	}
	if !reflect.DeepEqual(errorLines, wantLines) {
		t.Fatalf("Reconcile error lines\ngot:  %q\nwant: %q", errorLines, wantLines)
	}

	wantRules := expectedRules(allHealthyRules(module.cfg), 100, 200)
	if got := providerRules(ctx, t, module); !reflect.DeepEqual(got, wantRules) {
		t.Fatalf("kernel provider rules\ngot:  %v\nwant: %v", got, wantRules)
	}
	wantGateways := map[int][]string{
		100: {"192.0.2.1 dev att0", "2001:db8:1::1 dev att0"},
		200: {"203.0.113.1 dev webpass0", "2001:db8:2::1 dev webpass0"},
		300: nil,
	}
	for tableID, want := range wantGateways {
		if got := tableGateways(ctx, t, module, tableID); !reflect.DeepEqual(got, want) {
			t.Fatalf("table %d gateways = %q, want %q", tableID, got, want)
		}
	}

	routing := store.Snapshot().Routing
	wantCarrying := map[string]bool{"att": true, "webpass": true, "monkeybrains": false}
	for name, carrying := range wantCarrying {
		if routing[name].Carrying != carrying {
			t.Fatalf("%s carrying = %v, want %v", name, routing[name].Carrying, carrying)
		}
	}
}

// TestReconcileSteersPastAMissingLink runs a pass in a private network
// namespace where AT&T's and Webpass's links exist with default routes and
// Monkeybrains' link does not.
func TestReconcileSteersPastAMissingLink(t *testing.T) {
	t.Parallel()
	enterPrivateNetworkNamespace(t)

	ctx := context.Background()
	store := wanstate.New()
	module := newNamespaceModule(t, store)
	buildProviderLinks(ctx, t, module)

	reconcileWithMonkeybrainsMissing(ctx, t, module, store)
}

// TestReconcileRemovesAMissingProvidersRules installs Monkeybrains' rules from
// an earlier pass before its link disappears. The next pass must remove them
// while installing the present providers' rules.
func TestReconcileRemovesAMissingProvidersRules(t *testing.T) {
	t.Parallel()
	enterPrivateNetworkNamespace(t)

	ctx := context.Background()
	store := wanstate.New()
	module := newNamespaceModule(t, store)
	buildProviderLinks(ctx, t, module)
	var earlierRules []netif.DesiredRule
	for _, rule := range allHealthyRules(module.cfg) {
		if rule.TableID == 300 {
			earlierRules = append(earlierRules, rule)
		}
	}
	if err := netif.ReconcileRules(ctx, module.Log, earlierRules); err != nil {
		t.Fatalf("install monkeybrains rules: %v", err)
	}
	if got, want := providerRules(ctx, t, module), expectedRules(earlierRules, 300); !reflect.DeepEqual(got, want) {
		t.Fatalf("kernel rules before the pass = %v, want the monkeybrains rules %v", got, want)
	}

	reconcileWithMonkeybrainsMissing(ctx, t, module, store)
}
