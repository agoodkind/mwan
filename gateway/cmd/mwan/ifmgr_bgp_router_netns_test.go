//go:build linux && firewallnetns

package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/agoodkind/netlink"
	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/osrg/gobgp/v4/pkg/server"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	bgpRuntimePort         = 179
	bgpRuntimeKeepalive    = 1
	bgpRuntimeHold         = 3
	bgpRuntimeRouterMetric = 200
)

// A nonzero restartTime enables graceful restart for the neighbor. After the
// session ends, the router GoBGP server marks the gateway paths stale instead
// of withdrawing them, and the router does not delete its kernel routes. The
// GoBGP server withdraws the stale paths after the restart time or after the
// gateway End-of-RIB marker.
type bgpRuntimeRouter struct {
	server      *server.BgpServer
	handle      *netlink.Handle
	device      int
	neighbor    netip.Addr
	neighborASN uint32
	restartTime uint32

	mu        sync.Mutex
	installed map[string]*netlink.Route
	receipts  []time.Time
}

func startBGPRuntimeRouter(t *testing.T, namespace netns.NsHandle, device, routerID string, asn uint32, listen, neighbor netip.Addr, neighborASN uint32) *bgpRuntimeRouter {
	t.Helper()
	return startBGPRuntimeRouterWithRestart(t, namespace, device, routerID, asn, listen, neighbor, neighborASN, 0)
}

func startBGPRuntimeRouterWithRestart(t *testing.T, namespace netns.NsHandle, device, routerID string, asn uint32, listen, neighbor netip.Addr, neighborASN, restartTime uint32) *bgpRuntimeRouter {
	t.Helper()
	handle, err := netlink.NewHandleAt(namespace)
	if err != nil {
		t.Fatalf("open the router namespace: %v", err)
	}
	t.Cleanup(func() { _ = handle.Close() })
	link, err := handle.LinkByName(device)
	if err != nil {
		t.Fatalf("find the router link %s: %v", device, err)
	}
	bgpServer := server.NewBgpServer()
	entered := make(chan error, 1)
	go func() {
		// GoBGP opens the listening socket on the goroutine that runs Serve.
		runtime.LockOSThread()
		enterErr := netns.Set(namespace)
		entered <- enterErr
		if enterErr == nil {
			bgpServer.Serve()
		}
	}()
	if err := <-entered; err != nil {
		t.Fatalf("enter the router namespace: %v", err)
	}
	global := &apipb.Global{Asn: asn, RouterId: routerID, ListenPort: bgpRuntimePort, ListenAddresses: []string{listen.String()}}
	if err := bgpServer.StartBgp(t.Context(), &apipb.StartBgpRequest{Global: global}); err != nil {
		t.Fatalf("start the router BGP server: %v", err)
	}
	t.Cleanup(bgpServer.Stop)
	router := &bgpRuntimeRouter{
		server: bgpServer, handle: handle, device: link.Attrs().Index, neighbor: neighbor, neighborASN: neighborASN,
		restartTime: restartTime, installed: make(map[string]*netlink.Route),
	}
	callbacks := server.WatchEventMessageCallbacks{
		OnBestPath: func(paths []*apiutil.Path, _ time.Time) { router.apply(t, paths) },
		OnPeerUpdate: func(event *apiutil.WatchEventMessage_PeerEvent, _ time.Time) {
			if restartTime == 0 && event.Type == apiutil.PEER_EVENT_STATE && event.Peer.State.SessionState != bgppkt.BGP_FSM_ESTABLISHED {
				router.flush(t)
			}
		},
	}
	if err := bgpServer.WatchEvent(t.Context(), callbacks, server.WatchPeer(), server.WatchBestPath(true)); err != nil {
		t.Fatalf("watch the router BGP events: %v", err)
	}
	router.addNeighbor(t)
	return router
}

func (r *bgpRuntimeRouter) apply(t *testing.T, paths []*apiutil.Path) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, path := range paths {
		if path.Family != bgppkt.RF_IPv6_UC || path.Nlri == nil || !path.PeerAddress.IsValid() {
			continue
		}
		_, destination, err := net.ParseCIDR(path.Nlri.String())
		if err != nil {
			t.Errorf("router received the invalid prefix %s: %v", path.Nlri, err)
			continue
		}
		route := &netlink.Route{Dst: destination, LinkIndex: r.device, Priority: bgpRuntimeRouterMetric, Protocol: unix.RTPROT_BGP}
		if path.Withdrawal {
			r.remove(t, route)
			continue
		}
		r.receipts = append(r.receipts, time.Now())
		for _, attribute := range path.Attrs {
			if nextHop, ok := attribute.(*bgppkt.PathAttributeMpReachNLRI); ok {
				route.Gw = net.IP(nextHop.Nexthop.AsSlice())
			}
		}
		if err := r.handle.RouteReplace(route); err != nil {
			t.Errorf("router install %s: %v", destination, err)
			continue
		}
		r.installed[destination.String()] = route
	}
}

func (r *bgpRuntimeRouter) remove(t *testing.T, route *netlink.Route) {
	delete(r.installed, route.Dst.String())
	if err := r.handle.RouteDel(route); err != nil && !errors.Is(err, unix.ESRCH) {
		t.Errorf("router remove %s: %v", route.Dst, err)
	}
}

func (r *bgpRuntimeRouter) flush(t *testing.T) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, route := range r.installed {
		r.remove(t, route)
	}
}

func (r *bgpRuntimeRouter) addNeighbor(t *testing.T) {
	t.Helper()
	peer := &apipb.Peer{
		Conf:      &apipb.PeerConf{NeighborAddress: r.neighbor.String(), PeerAsn: r.neighborASN},
		Transport: &apipb.Transport{PassiveMode: true},
		Timers:    &apipb.Timers{Config: &apipb.TimersConfig{KeepaliveInterval: bgpRuntimeKeepalive, HoldTime: bgpRuntimeHold}},
		AfiSafis: []*apipb.AfiSafi{{Config: &apipb.AfiSafiConfig{
			Family: &apipb.Family{Afi: apipb.Family_AFI_IP6, Safi: apipb.Family_SAFI_UNICAST}, Enabled: true,
		}}},
	}
	if r.restartTime > 0 {
		peer.GracefulRestart = &apipb.GracefulRestart{Enabled: true, RestartTime: r.restartTime}
		peer.AfiSafis[0].MpGracefulRestart = &apipb.MpGracefulRestart{Config: &apipb.MpGracefulRestartConfig{Enabled: true}}
	}
	if err := r.server.AddPeer(t.Context(), &apipb.AddPeerRequest{Peer: peer}); err != nil {
		t.Fatalf("add the gateway as a router neighbor: %v", err)
	}
}

func (r *bgpRuntimeRouter) deleteNeighbor(t *testing.T) {
	t.Helper()
	if err := r.server.DeletePeer(t.Context(), &apipb.DeletePeerRequest{Address: r.neighbor.String()}); err != nil {
		t.Fatalf("delete the gateway as a router neighbor: %v", err)
	}
}

func (r *bgpRuntimeRouter) announce(t *testing.T, prefix netip.Prefix, nextHop netip.Addr) {
	t.Helper()
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		t.Fatalf("build NLRI for %s: %v", prefix, err)
	}
	nextHopAttribute, err := bgppkt.NewPathAttributeMpReachNLRI(bgppkt.RF_IPv6_UC, []bgppkt.PathNLRI{{NLRI: nlri}}, nextHop)
	if err != nil {
		t.Fatalf("build MP_REACH_NLRI for %s: %v", prefix, err)
	}
	path := &apiutil.Path{Family: bgppkt.RF_IPv6_UC, Nlri: nlri, Attrs: []bgppkt.PathAttributeInterface{
		bgppkt.NewPathAttributeOrigin(bgppkt.BGP_ORIGIN_ATTR_TYPE_IGP), nextHopAttribute,
	}}
	if _, err := r.server.AddPath(apiutil.AddPathRequest{Paths: []*apiutil.Path{path}}); err != nil {
		t.Fatalf("router announce %s: %v", prefix, err)
	}
}

func (r *bgpRuntimeRouter) withdraw(t *testing.T, prefix netip.Prefix) {
	t.Helper()
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		t.Fatalf("build NLRI for %s: %v", prefix, err)
	}
	path := &apiutil.Path{Family: bgppkt.RF_IPv6_UC, Nlri: nlri, Withdrawal: true}
	if err := r.server.DeletePath(apiutil.DeletePathRequest{Paths: []*apiutil.Path{path}}); err != nil {
		t.Fatalf("router withdraw %s: %v", prefix, err)
	}
}

// received fails the test when the router cannot list its Adj-RIB-In. An
// empty result comes only from a successful listing.
func (r *bgpRuntimeRouter) received(t *testing.T) []netip.Prefix {
	t.Helper()
	prefixes, err := r.listReceived()
	if err != nil {
		t.Fatalf("list the paths that the router received from %s: %v", r.neighbor, err)
	}
	return prefixes
}

func (r *bgpRuntimeRouter) listReceived() ([]netip.Prefix, error) {
	request := apiutil.ListPathRequest{TableType: apipb.TableType_TABLE_TYPE_ADJ_IN, Name: r.neighbor.String(), Family: bgppkt.RF_IPv6_UC}
	prefixes := make([]netip.Prefix, 0)
	err := r.server.ListPath(request, func(nlri bgppkt.NLRI, _ []*apiutil.Path) {
		prefixes = append(prefixes, netip.MustParsePrefix(nlri.String()))
	})
	if err != nil {
		return nil, fmt.Errorf("list the router Adj-RIB-In: %w", err)
	}
	slices.SortFunc(prefixes, netip.Prefix.Compare)
	return prefixes, nil
}

func (r *bgpRuntimeRouter) kernelRoute(t *testing.T, prefix netip.Prefix) bool {
	t.Helper()
	routes, err := r.handle.RouteListFiltered(netlink.FAMILY_V6, &netlink.Route{Protocol: unix.RTPROT_BGP}, netlink.RT_FILTER_PROTOCOL)
	if err != nil {
		t.Fatalf("list the router BGP routes: %v", err)
	}
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() == prefix.String() {
			return true
		}
	}
	return false
}
