//go:build linux && (firewall || protocol || systemd)

package testenv

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

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/osrg/gobgp/v4/pkg/server"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	BgpRuntimePort         = 179
	BgpRuntimeKeepalive    = 1
	BgpRuntimeHold         = 3
	BgpRuntimeRouterMetric = 200
)

type BgpRuntimeRouter struct {
	Server      *server.BgpServer
	Handle      *netlink.Handle
	Device      int
	Neighbor    netip.Addr
	NeighborASN uint32
	RestartTime uint32

	Mu           sync.Mutex
	Installed    map[string]*netlink.Route
	RetainOnStop bool
	GracefulStop bool
	Receipts     []time.Time
}

func StartBGPRuntimeRouter(
	t *testing.T,
	namespace netns.NsHandle,
	device string,
	routerID string,
	asn uint32,
	listen netip.Addr,
	neighbor netip.Addr,
	neighborASN uint32,
) *BgpRuntimeRouter {
	t.Helper()
	return StartBGPRuntimeRouterWithRestart(
		t,
		namespace,
		device,
		routerID,
		asn,
		listen,
		neighbor,
		neighborASN,
		0,
	)
}

func StartBGPRuntimeRouterWithRestart(
	t *testing.T,
	namespace netns.NsHandle,
	device string,
	routerID string,
	asn uint32,
	listen netip.Addr,
	neighbor netip.Addr,
	neighborASN uint32,
	restartTime uint32,
) *BgpRuntimeRouter {
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
	global := &apipb.Global{
		Asn:             asn,
		RouterId:        routerID,
		ListenPort:      BgpRuntimePort,
		ListenAddresses: []string{listen.String()},
	}
	if err := bgpServer.StartBgp(t.Context(), &apipb.StartBgpRequest{Global: global}); err != nil {
		t.Fatalf("start the router BGP server: %v", err)
	}
	t.Cleanup(bgpServer.Stop)
	router := &BgpRuntimeRouter{
		Server:      bgpServer,
		Handle:      handle,
		Device:      link.Attrs().Index,
		Neighbor:    neighbor,
		NeighborASN: neighborASN,
		RestartTime: restartTime,
		Installed:   make(map[string]*netlink.Route),
	}
	callbacks := server.WatchEventMessageCallbacks{
		OnBestPath: func(paths []*apiutil.Path, _ time.Time) { router.Apply(t, paths) },
		OnPeerUpdate: func(event *apiutil.WatchEventMessage_PeerEvent, _ time.Time) {
			if event.Type != apiutil.PEER_EVENT_STATE ||
				event.Peer.State.SessionState == bgppkt.BGP_FSM_ESTABLISHED {
				return
			}
			router.Mu.Lock()
			defer router.Mu.Unlock()
			if router.RetainOnStop {
				if event.Peer.State.DisconnectReason == apipb.PeerState_DISCONNECT_REASON_GRACEFUL_RESTART {
					router.GracefulStop = true
				}
				return
			}
			for _, route := range router.Installed {
				router.Remove(t, route)
			}
		},
	}
	if err := bgpServer.WatchEvent(
		t.Context(),
		callbacks,
		server.WatchPeer(),
		server.WatchBestPath(true),
	); err != nil {
		t.Fatalf("watch the router BGP events: %v", err)
	}
	router.AddNeighbor(t)
	return router
}

func (r *BgpRuntimeRouter) Apply(t *testing.T, paths []*apiutil.Path) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	for _, path := range paths {
		if path.Family != bgppkt.RF_IPv6_UC || path.Nlri == nil || !path.PeerAddress.IsValid() {
			continue
		}
		_, destination, err := net.ParseCIDR(path.Nlri.String())
		if err != nil {
			t.Errorf("router received the invalid prefix %s: %v", path.Nlri, err)
			continue
		}
		route := &netlink.Route{
			Dst:       destination,
			LinkIndex: r.Device,
			Priority:  BgpRuntimeRouterMetric,
			Protocol:  unix.RTPROT_BGP,
		}
		if path.Withdrawal {
			if !r.RetainOnStop {
				r.Remove(t, route)
			}
			continue
		}
		r.Receipts = append(r.Receipts, time.Now())
		for _, attribute := range path.Attrs {
			if nextHop, ok := attribute.(*bgppkt.PathAttributeMpReachNLRI); ok {
				route.Gw = net.IP(nextHop.Nexthop.AsSlice())
			}
		}
		if err := r.Handle.RouteReplace(route); err != nil {
			t.Errorf("router install %s: %v", destination, err)
			continue
		}
		r.Installed[destination.String()] = route
		if r.GracefulStop && !path.Stale {
			r.RetainOnStop = false
		}
	}
}

func (r *BgpRuntimeRouter) Remove(t *testing.T, route *netlink.Route) {
	delete(r.Installed, route.Dst.String())
	if err := r.Handle.RouteDel(route); err != nil && !errors.Is(err, unix.ESRCH) {
		t.Errorf("router remove %s: %v", route.Dst, err)
	}
}

func (r *BgpRuntimeRouter) Flush(t *testing.T) {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	for _, route := range r.Installed {
		r.Remove(t, route)
	}
}

func (r *BgpRuntimeRouter) AddNeighbor(t *testing.T) {
	t.Helper()
	peer := &apipb.Peer{
		Conf:      &apipb.PeerConf{NeighborAddress: r.Neighbor.String(), PeerAsn: r.NeighborASN},
		Transport: &apipb.Transport{PassiveMode: true},
		Timers: &apipb.Timers{
			Config: &apipb.TimersConfig{
				KeepaliveInterval: BgpRuntimeKeepalive,
				HoldTime:          BgpRuntimeHold,
			},
		},
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
	if r.RestartTime > 0 {
		peer.GracefulRestart = &apipb.GracefulRestart{
			Enabled:     true,
			RestartTime: r.RestartTime,
		}
		peer.AfiSafis[0].MpGracefulRestart = &apipb.MpGracefulRestart{
			Config: &apipb.MpGracefulRestartConfig{Enabled: true},
		}
	}
	if err := r.Server.AddPeer(t.Context(), &apipb.AddPeerRequest{Peer: peer}); err != nil {
		t.Fatalf("add the gateway as a router neighbor: %v", err)
	}
}

func (r *BgpRuntimeRouter) DeleteNeighbor(t *testing.T) {
	t.Helper()
	if err := r.Server.DeletePeer(
		t.Context(),
		&apipb.DeletePeerRequest{Address: r.Neighbor.String()},
	); err != nil {
		t.Fatalf("delete the gateway as a router neighbor: %v", err)
	}
	r.Flush(t)
}

func (r *BgpRuntimeRouter) Announce(t *testing.T, prefix netip.Prefix, nextHop netip.Addr) {
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
		t.Fatalf("router announce %s: %v", prefix, err)
	}
}

func (r *BgpRuntimeRouter) Withdraw(t *testing.T, prefix netip.Prefix) {
	t.Helper()
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		t.Fatalf("build NLRI for %s: %v", prefix, err)
	}
	path := &apiutil.Path{Family: bgppkt.RF_IPv6_UC, Nlri: nlri, Withdrawal: true}
	if err := r.Server.DeletePath(apiutil.DeletePathRequest{
		Paths: []*apiutil.Path{path},
	}); err != nil {
		t.Fatalf("router withdraw %s: %v", prefix, err)
	}
}

func (r *BgpRuntimeRouter) Received(t *testing.T) []netip.Prefix {
	t.Helper()
	prefixes, err := r.ListReceived()
	if err != nil {
		t.Fatalf("list the paths that the router received from %s: %v", r.Neighbor, err)
	}
	return prefixes
}

func (r *BgpRuntimeRouter) ListReceived() ([]netip.Prefix, error) {
	request := apiutil.ListPathRequest{
		TableType: apipb.TableType_TABLE_TYPE_ADJ_IN,
		Name:      r.Neighbor.String(),
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
		return nil, fmt.Errorf("list the router Adj-RIB-In: %w", err)
	}
	slices.SortFunc(prefixes, netip.Prefix.Compare)
	return prefixes, nil
}

func (r *BgpRuntimeRouter) KernelRoute(t *testing.T, prefix netip.Prefix) bool {
	t.Helper()
	routes, err := r.Handle.RouteListFiltered(
		netlink.FAMILY_V6,
		&netlink.Route{Protocol: unix.RTPROT_BGP},
		netlink.RT_FILTER_PROTOCOL,
	)
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

func (r *BgpRuntimeRouter) RetainRoutesOnStop() {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	r.RetainOnStop = true
	r.GracefulStop = false
}

func (r *BgpRuntimeRouter) ObservedGracefulStop() bool {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	return r.GracefulStop
}

func (r *BgpRuntimeRouter) HasFreshPath(t *testing.T, prefix netip.Prefix) bool {
	t.Helper()
	request := apiutil.ListPathRequest{
		TableType: apipb.TableType_TABLE_TYPE_ADJ_IN,
		Name:      r.Neighbor.String(),
		Family:    bgppkt.RF_IPv6_UC,
	}
	fresh := false
	if err := r.Server.ListPath(
		request,
		func(nlri bgppkt.NLRI, paths []*apiutil.Path) {
			if nlri.String() == prefix.String() {
				fresh = slices.ContainsFunc(
					paths,
					func(path *apiutil.Path) bool { return !path.Stale },
				)
			}
		},
	); err != nil {
		t.Fatalf("list the router Adj-RIB-In: %v", err)
	}
	return fresh
}
