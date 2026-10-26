//go:build linux && netns

package bgp_test

import (
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/agoodkind/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
)

func TestSessionRetryInstallsUnresolvedPathAndRemovesRejection(t *testing.T) {
	if !nextHopNamespace(t) {
		return
	}
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatalf("find the loopback link: %v", err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatalf("set the loopback link up: %v", err)
	}
	link := addDummyLink(t, "bgpext0", "2001:db8:ffff::1/64")
	transport := netip.MustParseAddr("2001:db8:ffff::1")
	neighbor := netip.MustParseAddr("2001:db8:ffff::2")
	learned := netip.MustParsePrefix("2001:db8:100::/48")
	remote := netip.MustParsePrefix("2001:db8:50::/64")
	nextHop := netip.MustParseAddr("2001:db8:50::1")

	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", transport, transport, sessionASN)
	cfg := baseSessionConfig("retry", externalPeerASN, peer)
	cfg.LocalAddress = transport
	cfg.Interface = link.Attrs().Name
	cfg.Tables = []int{sessionRouteTable}
	cfg.RouteMetric = sessionRouteMetric
	cfg.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48}}
	session := startSession(t, cfg)
	waitEstablished(t, session)

	peer.announce(t, learned, nextHop)
	eventually(t, "the session to reject the path without a route to the next hop", func() bool {
		state := session.State()
		return len(state.Accepted) == 0 && len(state.Rejected) == 1 &&
			strings.Contains(state.Rejected[0].Reason, "kernel route installation failed")
	})
	assertNoKernelRoute(t, learned, link)

	addNextHopRoute(t, link, unix.RT_TABLE_MAIN, remote, neighbor)
	session.RefreshRoutes(t.Context())
	state := session.State()
	if !slices.Equal(state.Accepted, []netip.Prefix{learned}) || len(state.Rejected) != 0 {
		t.Fatalf("state after the retry = accepted %v rejected %v, want %s accepted and no rejection", state.Accepted, state.Rejected, learned)
	}
	assertKernelRoute(t, learned, link, neighbor, sessionRouteMetric)
}
