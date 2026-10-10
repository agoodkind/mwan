//go:build linux && netns

package bgp_test

import (
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"

	apipb "github.com/osrg/gobgp/v4/api"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
)

const kernelDefaultIPv4Metric = 0

func TestSpeakerInstallsLearnedRoutesInKernel(t *testing.T) {
	const childEnv = "MWAN_BGP_SPEAKER_KERNEL_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestSpeakerInstallsLearnedRoutesInKernel$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated speaker kernel test child process failed: %v: %s", err, output)
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
	internalLink := addDummyLink(t, "bgpint0", "192.0.2.1/29", "2001:db8:ffff::1/64")

	transport := netip.MustParseAddr("2001:db8:ffff::1")
	ipv4Transit := netip.MustParsePrefix("192.0.2.0/29")
	ipv6Transit := netip.MustParsePrefix("2001:db8:ffff::/64")
	ipv4NextHop := netip.MustParseAddr("192.0.2.2")
	ipv6NextHop := netip.MustParseAddr("2001:db8:ffff::2")
	ipv4Default := netip.MustParsePrefix("0.0.0.0/0")
	ipv6Default := netip.MustParsePrefix("::/0")
	ipv4Learned := netip.MustParsePrefix("198.51.100.0/24")
	ipv6Learned := netip.MustParsePrefix("2001:db8:5::/48")
	ipv4Retained := netip.MustParsePrefix("203.0.113.0/25")
	ipv6Retained := netip.MustParsePrefix("2001:db8:6::/48")
	ipv4Leftover := netip.MustParsePrefix("203.0.113.128/25")
	ipv6Leftover := netip.MustParsePrefix("2001:db8:7::/48")

	fibConfig := bgp.FIBConfig{Tables: []int{sessionRouteTable}, InternalIface: internalLink.Attrs().Name}
	previousFIB := bgp.NewFIB(fibConfig, sessionLogger())
	for _, prefix := range []netip.Prefix{ipv4Leftover, ipv4Default} {
		applyFIB(t, previousFIB, bgp.PathEvent{Peer: "previous", Prefix: prefix, NextHop: ipv4NextHop})
	}
	for _, prefix := range []netip.Prefix{ipv6Leftover, ipv6Default} {
		applyFIB(t, previousFIB, bgp.PathEvent{Peer: "previous", Prefix: prefix, NextHop: ipv6NextHop})
	}

	fib := bgp.NewFIB(fibConfig, sessionLogger())
	fib.ArmSweep()
	listenPort := unusedPort(t, transport)
	speaker := bgp.New(bgp.Config{
		Enabled:                 true,
		ASN:                     sessionASN,
		RouterID:                sessionRouterID,
		KeepaliveSeconds:        keepaliveSeconds,
		HoldSeconds:             holdSeconds,
		ListenPort:              int32(listenPort),
		DynamicNeighborPrefixes: []netip.Prefix{ipv6Transit},
	}, sessionLogger())
	speaker.SetFIB(fib)
	if err := speaker.Start(t.Context()); err != nil {
		t.Fatalf("start speaker: %v", err)
	}
	t.Cleanup(func() {
		if err := speaker.Stop(); err != nil {
			t.Errorf("stop speaker: %v", err)
		}
	})
	peer := startTestPeerWithOptions(t, testPeerOptions{
		asn:          sessionASN,
		routerID:     "192.0.2.2",
		listen:       transport,
		neighbor:     transport,
		neighborASN:  sessionASN,
		neighborPort: listenPort,
		families:     []apipb.Family_Afi{apipb.Family_AFI_IP, apipb.Family_AFI_IP6},
	})
	eventually(t, "speaker peer session established", func() bool {
		return speaker.IsEstablished(t.Context())
	})

	peer.announce(t, ipv4Default, ipv4NextHop)
	peer.announce(t, ipv6Default, ipv6NextHop)
	peer.announce(t, ipv4Transit, ipv4NextHop)
	peer.announce(t, ipv6Transit, ipv6NextHop)
	peer.announce(t, ipv4Learned, ipv4NextHop)
	peer.announce(t, ipv6Learned, ipv6NextHop)
	peer.announce(t, ipv4Retained, ipv4NextHop)
	peer.announce(t, ipv6Retained, ipv6NextHop)
	eventually(t, "announced routes installed in kernel", func() bool {
		return hasKernelRoute(t, ipv4Retained, internalLink) && hasKernelRoute(t, ipv6Retained, internalLink)
	})
	assertKernelRoute(t, ipv4Learned, internalLink, ipv4NextHop, kernelDefaultIPv4Metric)
	assertKernelRoute(t, ipv6Learned, internalLink, ipv6NextHop, kernelDefaultIPv6Metric)
	assertKernelRoute(t, ipv4Retained, internalLink, ipv4NextHop, kernelDefaultIPv4Metric)
	assertKernelRoute(t, ipv6Retained, internalLink, ipv6NextHop, kernelDefaultIPv6Metric)
	assertNoKernelRoute(t, ipv4Transit, internalLink)
	assertNoKernelRoute(t, ipv6Transit, internalLink)

	if err := speaker.SweepStale(t.Context()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	guarded := []netip.Prefix{ipv4Default, ipv6Default, ipv4Transit, ipv6Transit, ipv4Leftover, ipv6Leftover}
	for _, prefix := range guarded {
		assertNoKernelRoute(t, prefix, internalLink)
	}
	assertKernelRoute(t, ipv4Learned, internalLink, ipv4NextHop, kernelDefaultIPv4Metric)
	assertKernelRoute(t, ipv6Learned, internalLink, ipv6NextHop, kernelDefaultIPv6Metric)

	peer.withdraw(t, ipv4Default)
	peer.withdraw(t, ipv6Default)
	peer.withdraw(t, ipv4Learned)
	peer.withdraw(t, ipv6Learned)
	eventually(t, "the kernel route to disappear after the peer withdrawal", func() bool {
		return !hasKernelRoute(t, ipv4Learned, internalLink) && !hasKernelRoute(t, ipv6Learned, internalLink)
	})
	assertKernelRoute(t, ipv4Retained, internalLink, ipv4NextHop, kernelDefaultIPv4Metric)
	assertKernelRoute(t, ipv6Retained, internalLink, ipv6NextHop, kernelDefaultIPv6Metric)

	peer.server.Stop()
	eventually(t, "the kernel route to disappear after peer loss", func() bool {
		return !hasKernelRoute(t, ipv4Retained, internalLink) && !hasKernelRoute(t, ipv6Retained, internalLink)
	})
}
