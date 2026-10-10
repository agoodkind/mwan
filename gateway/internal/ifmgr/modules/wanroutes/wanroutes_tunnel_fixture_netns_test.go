//go:build linux && netns

package wanroutes

import (
	"context"
	"net"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

type tunnelRouteFixture struct {
	module      *Module
	store       *wanstate.Store
	links       *ifmgr.OwnedLinkResults
	applied     []netif.OwnedLinkResult
	repairs     chan string
	healthPath  string
	journalPath string
}

func openTunnelRouteJournal(t *testing.T, path string) *netif.OwnedStaticReconciler {
	t.Helper()
	journal, err := netif.NewOwnedStaticReconciler(path)
	if err != nil {
		t.Fatalf("open the ownership journal: %v", err)
	}
	return journal
}

func addTunnelRouteProvider(ctx context.Context, t *testing.T, fixture *tunnelRouteFixture, iface, address, gateway, gatewayMAC string, metric int) {
	t.Helper()
	addDummyLink(t, iface)
	if err := netif.ReconcileAddrs(ctx, fixture.module.Log, iface, []netif.AddrSpec{{CIDR: address}}); err != nil {
		t.Fatalf("address %s: %v", iface, err)
	}
	setTunnelRouteDefault(t, iface, gateway, metric, true)
	setTunnelRouteNeighbour(t, iface, gateway, gatewayMAC)
}

// The dummy link has no peer that answers ARP. The kernel transmits to a permanent neighbor entry.
func setTunnelRouteNeighbour(t *testing.T, iface, gateway, gatewayMAC string) {
	t.Helper()
	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	hardware, err := net.ParseMAC(gatewayMAC)
	if err != nil {
		t.Fatal(err)
	}
	neighbour := &netlink.Neigh{LinkIndex: link.Attrs().Index, State: netlink.NUD_PERMANENT, IP: net.ParseIP(gateway), HardwareAddr: hardware}
	if err := netlink.NeighSet(neighbour); err != nil {
		t.Fatalf("neighbour %s on %s: %v", gateway, iface, err)
	}
}

// One kernel operation changes the gateway of the main-table default route.
func replaceTunnelRouteDefault(t *testing.T, iface, gateway string, metric int) {
	t.Helper()
	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(gateway), Priority: metric, Table: unix.RT_TABLE_MAIN}
	if err := netlink.RouteReplace(route); err != nil {
		t.Fatalf("replace the main default on %s with %s: %v", iface, gateway, err)
	}
}

func setTunnelRouteDefault(t *testing.T, iface, gateway string, metric int, present bool) {
	t.Helper()
	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(gateway), Priority: metric, Table: unix.RT_TABLE_MAIN}
	if present {
		err = netlink.RouteAdd(route)
	} else {
		route.Dst = &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}
		err = netlink.RouteDel(route)
	}
	if err != nil {
		t.Fatalf("main default via %s on %s present=%t: %v", gateway, iface, present, err)
	}
}

// The fixture has two IPv4 providers and one 6in4 tunnel over one provider.
// The tunnel uses the provider with the less preferred main-table default route. The link manager
// creates the tunnel device.
func newTunnelRouteFixture(ctx context.Context, t *testing.T) *tunnelRouteFixture {
	t.Helper()
	fixture := &tunnelRouteFixture{
		store: wanstate.New(), links: &ifmgr.OwnedLinkResults{}, repairs: make(chan string, 256),
		healthPath:  filepath.Join(t.TempDir(), "mwan-health.state"),
		journalPath: filepath.Join(t.TempDir(), "owned-addresses.json"),
	}
	cfg := tunnelRouteConfig(fixture.healthPath, true)
	created, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture.module = created.(*Module)
	env := testEnvWithStore(fixture.store)
	env.OwnedLinks = fixture.links
	env.TunnelEndpointRoutes = openTunnelRouteJournal(t, fixture.journalPath)
	env.RequestReconcile = func(reason string) { fixture.repairs <- reason }
	fixture.module.InitBase(env, "module", moduleName)
	fixture.module.resolveNextHop = netif.NextHopResolves

	addDummyLink(t, tunnelRouteInternal)
	addTunnelRouteProvider(ctx, t, fixture, tunnelRouteUnderlay, tunnelRouteLocal+"/24", tunnelRouteGateway, tunnelRouteUnderMAC, tunnelRouteUnderMetric)
	addTunnelRouteProvider(ctx, t, fixture, tunnelRouteOther, "203.0.113.2/29", tunnelRouteOtherGW, tunnelRouteOtherMAC, tunnelRouteOtherMetric)

	reconciler, err := netif.NewOwnedLinkReconciler(filepath.Join(t.TempDir(), "links.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.applied, err = reconciler.Reconcile(ctx, fixture.module.Log, []interfaceintent.Connection{
		{ID: "isp", Name: tunnelRouteUnderlay, Owner: interfaceintent.OwnerExternal},
		{
			ID: tunnelRouteID, Name: tunnelRouteIface, Owner: interfaceintent.OwnerMWAN,
			Link: &interfaceintent.Link{Kind: interfaceintent.KindTunnel, Tunnel: tunnelRouteIntent()},
		},
	})
	if err != nil {
		t.Fatalf("create the tunnel: %v", err)
	}
	fixture.links.Replace(fixture.applied)
	if result, found := fixture.links.Get(tunnelRouteID); !found || result.Status != netif.OwnedLinkReady {
		t.Fatalf("tunnel link result = %+v (found=%t)", result, found)
	}
	if err := netif.ReconcileAddrs(ctx, fixture.module.Log, tunnelRouteIface, []netif.AddrSpec{{CIDR: "2001:db8:6::2/64"}}); err != nil {
		t.Fatalf("tunnel address: %v", err)
	}
	innerDefault := netif.RouteSpec{Family: familyV6, Dest: "default", Via: tunnelRouteInnerGW, Dev: tunnelRouteIface, TableID: unix.RT_TABLE_MAIN}
	if err := netif.ReconcileTableDefault(ctx, fixture.module.Log, innerDefault); err != nil {
		t.Fatalf("tunnel default route: %v", err)
	}
	translations := make(map[string]wanstate.MemberTranslation, len(cfg.WANs))
	for _, wan := range cfg.WANs {
		translations[wan.Key()] = wanstate.MemberTranslation{
			V4: wanstate.FamilyTranslation{Ready: wan.TranslationV4 != nil},
			V6: wanstate.FamilyTranslation{Ready: wan.TranslationV6 != nil},
		}
	}
	fixture.store.SetTranslation(translations)
	fixture.writeHealth(t, map[string]string{"isp": netif.HealthStateHealthy, "other": netif.HealthStateHealthy, tunnelRouteID: netif.HealthStateHealthy})
	return fixture
}
