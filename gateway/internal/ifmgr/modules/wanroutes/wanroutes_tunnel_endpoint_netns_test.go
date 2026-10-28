//go:build linux && netns

package wanroutes

import (
	"bytes"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	tunnelRouteSecondGW  = "192.0.2.254"
	tunnelRouteSecondMAC = "02:00:5e:00:53:03"
)

func underlayTableMatch(t *testing.T) netlink.Route {
	t.Helper()
	matched, err := netlink.RouteGetWithOptions(net.ParseIP(tunnelRouteRemote), &netlink.RouteGetOptions{Mark: tunnelRouteUnderMark, FIBMatch: true})
	if err != nil || len(matched) != 1 {
		t.Fatalf("lookup of %s with mark %d = %+v, %v", tunnelRouteRemote, tunnelRouteUnderMark, matched, err)
	}
	return matched[0]
}

func requireEndpointRouteSelected(t *testing.T, gateway string) {
	t.Helper()
	requireTunnelEndpointRoute(t, gateway)
	matched := underlayTableMatch(t)
	if matched.Dst == nil || matched.Dst.String() != tunnelRouteEndpoint || matched.Table != tunnelRouteUnderTbl ||
		!matched.Gw.Equal(net.ParseIP(gateway)) {
		t.Fatalf("lookup with the underlay mark matched %+v, want the endpoint route %s via %s in table %d",
			matched, tunnelRouteEndpoint, gateway, tunnelRouteUnderTbl)
	}
}

func requireOuterFrameTo(t *testing.T, gatewayMAC string) {
	t.Helper()
	underlayCapture := openTunnelRouteCapture(t, tunnelRouteUnderlay)
	otherCapture := openTunnelRouteCapture(t, tunnelRouteOther)
	sendThroughTunnel(t)
	frames := readOuterFrames(t, underlayCapture, 5*time.Second, true)
	wantMAC, err := net.ParseMAC(gatewayMAC)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 || !bytes.Equal(frames[0].destinationMAC, wantMAC) ||
		frames[0].source != netip.MustParseAddr(tunnelRouteLocal) || frames[0].destination != netip.MustParseAddr(tunnelRouteRemote) {
		t.Fatalf("outer frames on %s = %+v, want one protocol 41 frame from %s to %s through gateway %s",
			tunnelRouteUnderlay, frames, tunnelRouteLocal, tunnelRouteRemote, gatewayMAC)
	}
	if misrouted := readOuterFrames(t, otherCapture, 300*time.Millisecond, false); len(misrouted) != 0 {
		t.Fatalf("outer frames on %s = %+v, want none", tunnelRouteOther, misrouted)
	}
}

func TestTunnelEndpointRouteFollowsTheUnderlayGateway(t *testing.T) {
	if !enterTunnelRouteNamespace(t) {
		return
	}
	ctx := t.Context()
	fixture := newTunnelRouteFixture(ctx, t)
	setTunnelRouteNeighbour(t, tunnelRouteUnderlay, tunnelRouteSecondGW, tunnelRouteSecondMAC)

	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")
	requireEndpointRouteSelected(t, tunnelRouteGateway)
	if got := tableGateways(ctx, t, fixture.module, tunnelRouteTunnelTbl); len(got) != 1 || got[0] != tunnelRouteInnerGW+" dev "+tunnelRouteIface {
		t.Fatalf("tunnel table gateways = %q, want the IPv6 default through the tunnel", got)
	}
	other, err := netlink.LinkByName(tunnelRouteOther)
	if err != nil {
		t.Fatal(err)
	}
	preferred, err := netlink.RouteGet(net.ParseIP(tunnelRouteRemote))
	if err != nil || len(preferred) != 1 || preferred[0].LinkIndex != other.Attrs().Index {
		t.Fatalf("unbound lookup of the endpoint = %+v, %v; want the preferred default on %s", preferred, err, tunnelRouteOther)
	}
	requireOuterFrameTo(t, tunnelRouteUnderMAC)

	replaceTunnelRouteDefault(t, tunnelRouteUnderlay, tunnelRouteSecondGW, tunnelRouteUnderMetric)
	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")
	requireEndpointRouteSelected(t, tunnelRouteSecondGW)
	requireOuterFrameTo(t, tunnelRouteSecondMAC)

	setTunnelRouteDefault(t, tunnelRouteUnderlay, tunnelRouteSecondGW, tunnelRouteUnderMetric, false)
	withoutGateway := fixture.reconcile(ctx, t)
	requireTunnelRouting(t, withoutGateway, false, reasonUnderlayNotReady)
	if underlayRouting := withoutGateway.Routing["isp"]; underlayRouting.V4Ready || underlayRouting.V4Reason != reasonGatewayUnavailable {
		t.Fatalf("underlay routing without a gateway = %+v", underlayRouting)
	}
	for _, tableID := range []int{tunnelRouteUnderTbl, tunnelRouteTunnelTbl, tunnelRouteOtherTbl, unix.RT_TABLE_MAIN} {
		if endpoints := tunnelEndpointRoutes(t, tableID); len(endpoints) != 0 {
			t.Fatalf("table %d endpoint routes without an underlay gateway = %+v", tableID, endpoints)
		}
	}
	if matched := underlayTableMatch(t); matched.Dst != nil && matched.Dst.String() == tunnelRouteEndpoint {
		t.Fatalf("lookup with the underlay mark matched %+v without an underlay gateway", matched)
	}

	setTunnelRouteDefault(t, tunnelRouteUnderlay, tunnelRouteGateway, tunnelRouteUnderMetric, true)
	requireTunnelRouting(t, fixture.reconcile(ctx, t), true, "")
	requireEndpointRouteSelected(t, tunnelRouteGateway)
}
