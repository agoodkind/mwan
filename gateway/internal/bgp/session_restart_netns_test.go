//go:build linux && netns

package bgp_test

import (
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
)

func TestSessionRestartRetainsKernelRoutesUntilTheEndOfRIB(t *testing.T) {
	const childEnv = "MWAN_BGP_SESSION_RESTART_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated session restart test: %v: %s", err, output)
		}
		return
	}

	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatalf("find the loopback link: %v", err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatalf("set the loopback link up: %v", err)
	}
	sessionLink := addDummyLink(t, "bgpext0", "2001:db8:ffff::1/64")
	transport := netip.MustParseAddr("2001:db8:ffff::1")
	nextHop := netip.MustParseAddr("2001:db8:ffff::2")
	resent := netip.MustParsePrefix("2001:db8:100::/48")
	withdrawn := netip.MustParsePrefix("2001:db8:200::/48")

	peer := startGracefulTestPeer(t, transport)
	cfg := baseSessionConfig("restart", externalPeerASN, peer)
	cfg.LocalAddress = transport
	cfg.Interface = sessionLink.Attrs().Name
	cfg.Tables = []int{sessionRouteTable}
	cfg.RouteMetric = sessionRouteMetric
	cfg.GracefulRestartSeconds = sessionRestartSeconds
	cfg.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48}}

	first := startSession(t, cfg)
	waitEstablished(t, first)
	peer.announce(t, resent, nextHop)
	peer.announce(t, withdrawn, nextHop)
	eventually(t, "the first session to install both routes", func() bool {
		return hasKernelRoute(t, resent, sessionLink) && hasKernelRoute(t, withdrawn, sessionLink)
	})
	if err := first.StopRetaining(t.Context()); err != nil {
		t.Fatalf("StopRetaining: %v", err)
	}
	assertKernelRoute(t, resent, sessionLink, nextHop, sessionRouteMetric)
	assertKernelRoute(t, withdrawn, sessionLink, nextHop, sessionRouteMetric)
	peer.withdraw(t, withdrawn)

	restarted := restartKeepingRoutes(t, cfg)
	if got, want := restarted.State().Retained, []netip.Prefix{resent, withdrawn}; !slices.Equal(got, want) {
		t.Fatalf("restarted session retained prefixes = %v, want %v", got, want)
	}
	assertKernelRoute(t, withdrawn, sessionLink, nextHop, sessionRouteMetric)
	if err := restarted.Connect(t.Context()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	eventually(t, "the End-of-RIB marker to end the route retention", func() bool {
		state := restarted.State()
		return len(state.Retained) == 0 && slices.Equal(state.Accepted, []netip.Prefix{resent})
	})
	assertKernelRoute(t, resent, sessionLink, nextHop, sessionRouteMetric)
	assertNoKernelRoute(t, withdrawn, sessionLink)
}

func TestSessionDeletesRetainedKernelRoutesAtTheRestartDeadline(t *testing.T) {
	const childEnv = "MWAN_BGP_SESSION_DEADLINE_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated session deadline test: %v: %s", err, output)
		}
		return
	}

	sessionLink := addDummyLink(t, "bgpext0", "2001:db8:ffff::1/64")
	nextHop := netip.MustParseAddr("2001:db8:ffff::2")
	retained := netip.MustParsePrefix("2001:db8:100::/48")
	previousFIB := bgp.NewFIB(bgp.FIBConfig{
		Tables: []int{sessionRouteTable}, InternalIface: sessionLink.Attrs().Name, Metric: sessionRouteMetric,
	}, sessionLogger())
	applyFIB(t, previousFIB, bgp.PathEvent{Peer: "previous", Prefix: retained, NextHop: nextHop})

	cfg := bgp.SessionConfig{
		Name: "deadline", RouterID: netip.MustParseAddr(sessionRouterID), LocalASN: sessionASN, RemoteASN: externalPeerASN,
		PeerAddress: nextHop, PeerPort: 179, Interface: sessionLink.Attrs().Name,
		KeepaliveSeconds: keepaliveSeconds, HoldSeconds: holdSeconds, GracefulRestartSeconds: sessionRestartSeconds,
		Tables: []int{sessionRouteTable}, RouteMetric: sessionRouteMetric,
	}
	session, err := bgp.NewSession(cfg, sessionLogger())
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := session.KeepRoutes(t.Context(), time.Now().Add(500*time.Millisecond)); err != nil {
		t.Fatalf("KeepRoutes: %v", err)
	}
	if got := session.State().Retained; !slices.Equal(got, []netip.Prefix{retained}) {
		t.Fatalf("retained prefixes = %v, want %s", got, retained)
	}
	eventually(t, "the restart deadline to delete the retained route", func() bool {
		return !hasKernelRoute(t, retained, sessionLink) && len(session.State().Retained) == 0
	})
}
