//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const (
	BgpExpiryIPv6Header       = 40
	BgpExpiryICMPv6Header     = 8
	BgpExpiryNextHeader       = 6
	BgpExpiryICMPv6           = 58
	BgpExpiryTimeExceeded     = 3
	BgpExpiryTCPFlags         = 13
	BgpExpirySYN              = 0x02
	BgpExpiryFrame            = 2*BgpExpiryIPv6Header + BgpExpiryICMPv6Header + BgpExpiryTCPFlags + 1
	BgpMultihopSecondNeighbor = "2001:db8:6::3"
)

func OpenBGPExpiryCapture(t *testing.T, topology BgpUpstreamRuntime) int {
	t.Helper()
	SetRuntimeNamespace(t, topology.Endpoint)
	defer SetRuntimeNamespace(t, topology.Gateway)
	link, err := netlink.LinkByName("far6in4")
	if err != nil {
		t.Fatal(err)
	}
	protocol := uint16(unix.ETH_P_ALL<<8 | unix.ETH_P_ALL>>8)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_DGRAM|unix.SOCK_NONBLOCK, int(protocol))
	if err != nil {
		t.Fatalf("open the VPS tunnel capture: %v", err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := unix.Bind(
		fd,
		&unix.SockaddrLinklayer{Protocol: protocol, Ifindex: link.Attrs().Index},
	); err != nil {
		t.Fatalf("bind the VPS tunnel capture: %v", err)
	}
	return fd
}

func BgpExpiryCaptured(t *testing.T, fd int, source, destination netip.Addr) bool {
	t.Helper()
	buffer := make([]byte, BgpExpiryFrame)
	for {
		length, _, err := unix.Recvfrom(fd, buffer, unix.MSG_TRUNC)
		if errors.Is(err, unix.EAGAIN) {
			return false
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatalf("read the VPS tunnel capture: %v", err)
		}
		if length < BgpExpiryFrame || buffer[BgpExpiryNextHeader] != BgpExpiryICMPv6 ||
			buffer[BgpExpiryIPv6Header] != BgpExpiryTimeExceeded {
			continue
		}
		expired := buffer[BgpExpiryIPv6Header+BgpExpiryICMPv6Header:]
		segment := expired[BgpExpiryIPv6Header:]
		expiredSource, _ := netip.AddrFromSlice(expired[8:24])
		expiredDestination, _ := netip.AddrFromSlice(expired[24:40])
		port := int(segment[2])<<8 | int(segment[3])
		synchronize := segment[BgpExpiryTCPFlags]&BgpExpirySYN != 0
		if expired[BgpExpiryNextHeader] == TunnelRuntimeProtocolTCP && expiredSource == source &&
			expiredDestination == destination &&
			port == BgpRuntimePort &&
			synchronize {
			return true
		}
	}
}

func SetBGPMultihopSecondNeighbor(t *testing.T, present bool) {
	t.Helper()
	link, err := netlink.LinkByName(TunnelRuntimeDevice)
	if err != nil {
		t.Fatal(err)
	}
	_, destination, err := net.ParseCIDR(BgpUpstreamAddress + "/128")
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       destination,
		Gw:        net.ParseIP(BgpMultihopSecondNeighbor),
	}
	if present {
		err = netlink.RouteAdd(route)
	} else {
		err = netlink.RouteDel(route)
	}
	if err != nil {
		t.Fatalf("change the route to the upstream through %s: %v", BgpMultihopSecondNeighbor, err)
	}
}
