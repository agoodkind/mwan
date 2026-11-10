//go:build linux && netns

package bgp_test

import (
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
)

func TestSessionInstallsAcceptedRoutesInKernel(t *testing.T) {
	const childEnv = "MWAN_BGP_SESSION_KERNEL_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestSessionInstallsAcceptedRoutesInKernel$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated session kernel test: %v: %s", err, output)
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
	internalLink := addDummyLink(t, "bgpint0", "2001:db8:fffe::1/64")

	transport := netip.MustParseAddr("2001:db8:ffff::1")
	peerNextHop := netip.MustParseAddr("2001:db8:ffff::2")
	defaultRoute := netip.MustParsePrefix("::/0")
	allowed := netip.MustParsePrefix("2001:db8:100::/48")
	undeclared := netip.MustParsePrefix("3fff:100::/32")
	leftover := netip.MustParsePrefix("2001:db8:400::/48")
	exported := netip.MustParsePrefix("2001:db8:500::/48")
	internalPrefix := netip.MustParsePrefix("2001:db8:300::/48")
	internalNextHop := netip.MustParseAddr("2001:db8:fffe::2")

	// The internal speaker's installer writes BGP routes for another device in
	// the same table. The session also installs one of the BGP prefixes.
	internalFIB := bgp.NewFIB(
		bgp.FIBConfig{Tables: []int{sessionRouteTable}, InternalIface: internalLink.Attrs().Name},
		sessionLogger(),
	)
	internalFIB.ArmSweep()
	internalOnly := bgp.PathEvent{Peer: "internal", Prefix: internalPrefix, NextHop: internalNextHop}
	internalShared := bgp.PathEvent{Peer: "internal", Prefix: allowed, NextHop: internalNextHop}
	applyFIB(t, internalFIB, internalOnly)
	applyFIB(t, internalFIB, internalShared)
	// A BGP route from a previous process exists on the session device.
	previousFIB := bgp.NewFIB(
		bgp.FIBConfig{Tables: []int{sessionRouteTable}, InternalIface: sessionLink.Attrs().Name},
		sessionLogger(),
	)
	applyFIB(t, previousFIB, bgp.PathEvent{Peer: "previous", Prefix: leftover, NextHop: peerNextHop})

	peer := startTestPeer(t, externalPeerASN, "192.0.2.2", transport, transport, sessionASN)
	cfg := baseSessionConfig("kernel", externalPeerASN, peer)
	cfg.LocalAddress = transport
	cfg.Interface = sessionLink.Attrs().Name
	cfg.Tables = []int{sessionRouteTable}
	cfg.RouteMetric = sessionRouteMetric
	cfg.Import = []bgp.ImportRule{
		{Prefix: defaultRoute, MinLength: 0, MaxLength: 0},
		{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 32, MaxLength: 48},
	}
	cfg.Export = []bgp.ExportRule{{Prefix: exported, Mode: bgp.ExportAlways, NextHop: transport}}
	session := startSession(t, cfg)
	waitEstablished(t, session)

	peer.announce(t, defaultRoute, peerNextHop)
	peer.announce(t, allowed, peerNextHop)
	peer.announce(t, undeclared, peerNextHop)
	peer.announce(t, exported, peerNextHop)
	eventually(t, "the session to accept two prefixes and reject two", func() bool {
		state := session.State()
		return len(state.Accepted) == 2 && len(state.Rejected) == 2
	})
	assertNoKernelRoute(t, exported, sessionLink)
	peer.withdraw(t, exported)
	assertKernelRoute(t, defaultRoute, sessionLink, peerNextHop, sessionRouteMetric)
	assertKernelRoute(t, allowed, sessionLink, peerNextHop, sessionRouteMetric)
	assertKernelRoute(t, allowed, internalLink, internalNextHop, kernelDefaultIPv6Metric)
	assertNoKernelRoute(t, undeclared, sessionLink)

	if err := session.SweepStale(t.Context()); err != nil {
		t.Fatalf("sweep the session routes: %v", err)
	}
	if err := internalFIB.SweepStale(t.Context()); err != nil {
		t.Fatalf("sweep the internal routes: %v", err)
	}
	assertNoKernelRoute(t, leftover, sessionLink)
	assertNoKernelRoute(t, exported, sessionLink)
	assertKernelRoute(t, defaultRoute, sessionLink, peerNextHop, sessionRouteMetric)
	assertKernelRoute(t, allowed, sessionLink, peerNextHop, sessionRouteMetric)
	assertKernelRoute(t, allowed, internalLink, internalNextHop, kernelDefaultIPv6Metric)
	assertKernelRoute(t, internalPrefix, internalLink, internalNextHop, kernelDefaultIPv6Metric)

	internalWithdrawal := bgp.PathEvent{Peer: "internal", Prefix: allowed, Withdrawn: true}
	applyFIB(t, internalFIB, internalWithdrawal)
	assertNoKernelRoute(t, allowed, internalLink)
	assertKernelRoute(t, allowed, sessionLink, peerNextHop, sessionRouteMetric)
	applyFIB(t, internalFIB, internalShared)

	peer.withdraw(t, defaultRoute)
	eventually(t, "the default route to disappear after the peer withdrawal", func() bool {
		return !hasKernelRoute(t, defaultRoute, sessionLink)
	})
	assertKernelRoute(t, allowed, sessionLink, peerNextHop, sessionRouteMetric)

	// The kernel rejects a gateway that is an address of the host.
	peer.announceWithMED(t, allowed, transport, 1)
	eventually(t, "the session to reject the path with the unusable next hop", func() bool {
		return len(session.State().Accepted) == 0
	})
	var installReason string
	for _, rejection := range session.State().Rejected {
		if rejection.Prefix == allowed {
			installReason = rejection.Reason
		}
	}
	if !strings.Contains(installReason, "kernel route installation failed") {
		t.Errorf("failed installation reason = %q, want a kernel installation failure", installReason)
	}
	assertNoKernelRoute(t, allowed, sessionLink)
	assertKernelRoute(t, allowed, internalLink, internalNextHop, kernelDefaultIPv6Metric)

	peer.announce(t, allowed, peerNextHop)
	eventually(t, "the kernel route to return after a usable announcement", func() bool {
		return hasKernelRoute(t, allowed, sessionLink)
	})
	peer.withdraw(t, allowed)
	eventually(t, "the kernel route to disappear after the peer withdrawal", func() bool {
		return !hasKernelRoute(t, allowed, sessionLink)
	})
	assertKernelRoute(t, allowed, internalLink, internalNextHop, kernelDefaultIPv6Metric)

	peer.announce(t, allowed, peerNextHop)
	eventually(t, "the kernel route to return after the new announcement", func() bool {
		return hasKernelRoute(t, allowed, sessionLink)
	})
	peer.server.Stop()
	eventually(t, "the kernel route to disappear after peer loss", func() bool {
		return !hasKernelRoute(t, allowed, sessionLink)
	})
	assertKernelRoute(t, allowed, internalLink, internalNextHop, kernelDefaultIPv6Metric)
	assertKernelRoute(t, internalPrefix, internalLink, internalNextHop, kernelDefaultIPv6Metric)
}
