//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"errors"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/osrg/gobgp/v4/pkg/server"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

type BgpTransitNeighbor struct {
	Address     netip.Addr
	Asn         uint32
	MultihopTTL uint32
	Active      bool
}

type BgpTransitInstalled struct {
	Peer  netip.Addr
	Route *netlink.Route
}

type BgpTransitRouter struct {
	Server *server.BgpServer
	Handle *netlink.Handle

	Mu        sync.Mutex
	Installed map[string]BgpTransitInstalled
}

func StartBGPTransitRouter(
	t *testing.T,
	namespace netns.NsHandle,
	routerID string,
	asn uint32,
	listen []netip.Addr,
	neighbors ...BgpTransitNeighbor,
) *BgpTransitRouter {
	t.Helper()
	handle, err := netlink.NewHandleAt(namespace)
	if err != nil {
		t.Fatalf("open the transit router namespace: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	bgpServer := server.NewBgpServer()
	entered := make(chan error, 1)
	go func() {
		// Wait for Serve to open the GoBGP listening socket.
		runtime.LockOSThread()
		enterErr := netns.Set(namespace)
		entered <- enterErr
		if enterErr == nil {
			bgpServer.Serve()
		}
	}()
	if err := <-entered; err != nil {
		t.Fatalf("enter the transit router namespace: %v", err)
	}
	addresses := make([]string, 0, len(listen))
	for _, address := range listen {
		addresses = append(addresses, address.String())
	}
	global := &apipb.Global{
		Asn:             asn,
		RouterId:        routerID,
		ListenPort:      BgpRuntimePort,
		ListenAddresses: addresses,
	}
	if err := bgpServer.StartBgp(t.Context(), &apipb.StartBgpRequest{Global: global}); err != nil {
		t.Fatalf("start the transit router BGP server: %v", err)
	}
	t.Cleanup(bgpServer.Stop)
	router := &BgpTransitRouter{
		Server:    bgpServer,
		Handle:    handle,
		Installed: make(map[string]BgpTransitInstalled),
	}
	callbacks := server.WatchEventMessageCallbacks{
		OnBestPath: func(paths []*apiutil.Path, _ time.Time) { router.Apply(t, paths) },
		OnPeerUpdate: func(event *apiutil.WatchEventMessage_PeerEvent, _ time.Time) {
			if event.Type == apiutil.PEER_EVENT_STATE &&
				event.Peer.State.SessionState != bgppkt.BGP_FSM_ESTABLISHED {
				router.Flush(t, event.Peer.State.NeighborAddress)
			}
		},
	}
	if err := bgpServer.WatchEvent(
		t.Context(),
		callbacks,
		server.WatchPeer(),
		server.WatchBestPath(true),
	); err != nil {
		t.Fatalf("watch the transit router BGP events: %v", err)
	}
	for _, neighbor := range neighbors {
		router.AddNeighbor(t, neighbor)
	}
	return router
}

func (r *BgpTransitRouter) AddNeighbor(t *testing.T, neighbor BgpTransitNeighbor) {
	t.Helper()
	timers := &apipb.TimersConfig{
		KeepaliveInterval: BgpRuntimeKeepalive,
		HoldTime:          BgpRuntimeHold,
		ConnectRetry:      BgpRuntimeKeepalive,
	}
	peer := &apipb.Peer{
		Conf: &apipb.PeerConf{
			NeighborAddress: neighbor.Address.String(),
			PeerAsn:         neighbor.Asn,
		},
		Transport: &apipb.Transport{PassiveMode: !neighbor.Active},
		Timers:    &apipb.Timers{Config: timers},
		AfiSafis: []*apipb.AfiSafi{
			{
				Config: &apipb.AfiSafiConfig{
					Family: &apipb.Family{
						Afi:  apipb.Family_AFI_IP6,
						Safi: apipb.Family_SAFI_UNICAST,
					},
					Enabled: true,
				},
			},
		},
	}
	if neighbor.MultihopTTL > 0 {
		peer.EbgpMultihop = &apipb.EbgpMultihop{Enabled: true, MultihopTtl: neighbor.MultihopTTL}
	}
	if err := r.Server.AddPeer(t.Context(), &apipb.AddPeerRequest{Peer: peer}); err != nil {
		t.Fatalf("add the transit router neighbor %s: %v", neighbor.Address, err)
	}
}

func (r *BgpTransitRouter) Apply(t *testing.T, paths []*apiutil.Path) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	for _, path := range paths {
		if path.Family != bgppkt.RF_IPv6_UC || path.Nlri == nil || !path.PeerAddress.IsValid() {
			continue
		}
		_, destination, err := net.ParseCIDR(path.Nlri.String())
		if err != nil {
			t.Errorf("transit router received the invalid prefix %s: %v", path.Nlri, err)
			continue
		}
		if path.Withdrawal {
			r.Remove(t, destination.String())
			continue
		}
		route := &netlink.Route{
			Dst:      destination,
			Priority: BgpRuntimeRouterMetric,
			Protocol: unix.RTPROT_BGP,
		}
		for _, attribute := range path.Attrs {
			if nextHop, ok := attribute.(*bgppkt.PathAttributeMpReachNLRI); ok {
				route.Gw = net.IP(nextHop.Nexthop.AsSlice())
			}
		}
		resolved, err := r.Handle.RouteGet(route.Gw)
		if err != nil || len(resolved) == 0 {
			t.Errorf("transit router resolve the next hop %s of %s: %v", route.Gw, destination, err)
			continue
		}
		route.LinkIndex = resolved[0].LinkIndex
		if resolved[0].Gw != nil {
			route.Gw = resolved[0].Gw
		}
		if err := r.Handle.RouteReplace(route); err != nil {
			t.Errorf("transit router install %s: %v", destination, err)
			continue
		}
		r.Installed[destination.String()] = BgpTransitInstalled{
			Peer:  path.PeerAddress,
			Route: route,
		}
	}
}

func (r *BgpTransitRouter) Remove(t *testing.T, destination string) {
	installed, found := r.Installed[destination]
	if !found {
		return
	}
	delete(r.Installed, destination)
	if err := r.Handle.RouteDel(installed.Route); err != nil && !errors.Is(err, unix.ESRCH) {
		t.Errorf("transit router remove %s: %v", destination, err)
	}
}

func (r *BgpTransitRouter) Flush(t *testing.T, peer netip.Addr) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	for destination, installed := range r.Installed {
		if installed.Peer == peer {
			r.Remove(t, destination)
		}
	}
}

func (r *BgpTransitRouter) Announce(t *testing.T, prefix netip.Prefix, nextHop netip.Addr) {
	t.Helper()
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		t.Fatalf("build NLRI for %s: %v", prefix, err)
	}
	nextHopAttribute, err := bgppkt.NewPathAttributeMpReachNLRI(
		bgppkt.RF_IPv6_UC,
		[]bgppkt.PathNLRI{{NLRI: nlri}},
		nextHop,
	)
	if err != nil {
		t.Fatalf("build MP_REACH_NLRI for %s: %v", prefix, err)
	}
	path := &apiutil.Path{
		Family: bgppkt.RF_IPv6_UC,
		Nlri:   nlri,
		Attrs: []bgppkt.PathAttributeInterface{
			bgppkt.NewPathAttributeOrigin(bgppkt.BGP_ORIGIN_ATTR_TYPE_IGP),
			nextHopAttribute,
		},
	}
	if _, err := r.Server.AddPath(apiutil.AddPathRequest{
		Paths: []*apiutil.Path{path},
	}); err != nil {
		t.Fatalf("transit router announce %s: %v", prefix, err)
	}
}

func (r *BgpTransitRouter) Received(t *testing.T, neighbor netip.Addr) []netip.Prefix {
	t.Helper()
	request := apiutil.ListPathRequest{
		TableType: apipb.TableType_TABLE_TYPE_ADJ_IN,
		Name:      neighbor.String(),
		Family:    bgppkt.RF_IPv6_UC,
	}
	prefixes := make([]netip.Prefix, 0)
	err := r.Server.ListPath(
		request,
		func(nlri bgppkt.NLRI, _ []*apiutil.Path) {
			prefixes = append(prefixes, netip.MustParsePrefix(nlri.String()))
		},
	)
	if err != nil {
		t.Fatalf("list the paths that the transit router received from %s: %v", neighbor, err)
	}
	slices.SortFunc(prefixes, netip.Prefix.Compare)
	return prefixes
}

func BgpNamespaceRoute(t *testing.T, handle *netlink.Handle, prefix netip.Prefix) bool {
	t.Helper()
	routes, err := handle.RouteListFiltered(
		netlink.FAMILY_V6,
		&netlink.Route{Protocol: unix.RTPROT_BGP},
		netlink.RT_FILTER_PROTOCOL,
	)
	if err != nil {
		t.Fatalf("list the namespace BGP routes: %v", err)
	}
	for _, route := range routes {
		destination := "::/0"
		if route.Dst != nil {
			destination = route.Dst.String()
		}
		if destination == prefix.String() {
			return true
		}
	}
	return false
}
