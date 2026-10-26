//go:build linux && firewallnetns

package main

import (
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/agoodkind/netlink"
	"golang.org/x/sys/unix"
)

const (
	bgpExpiryIPv6Header       = 40
	bgpExpiryICMPv6Header     = 8
	bgpExpiryNextHeader       = 6
	bgpExpiryICMPv6           = 58
	bgpExpiryTimeExceeded     = 3
	bgpExpiryTCPFlags         = 13
	bgpExpirySYN              = 0x02
	bgpExpiryFrame            = 2*bgpExpiryIPv6Header + bgpExpiryICMPv6Header + bgpExpiryTCPFlags + 1
	bgpMultihopSecondNeighbor = "2001:db8:6::3"
)

func openBGPExpiryCapture(t *testing.T, topology bgpUpstreamRuntime) int {
	t.Helper()
	setRuntimeNamespace(t, topology.endpoint)
	defer setRuntimeNamespace(t, topology.gateway)
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
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: protocol, Ifindex: link.Attrs().Index}); err != nil {
		t.Fatalf("bind the VPS tunnel capture: %v", err)
	}
	return fd
}

func bgpExpiryCaptured(t *testing.T, fd int, source, destination netip.Addr) bool {
	t.Helper()
	buffer := make([]byte, bgpExpiryFrame)
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
		if length < bgpExpiryFrame || buffer[bgpExpiryNextHeader] != bgpExpiryICMPv6 || buffer[bgpExpiryIPv6Header] != bgpExpiryTimeExceeded {
			continue
		}
		expired := buffer[bgpExpiryIPv6Header+bgpExpiryICMPv6Header:]
		segment := expired[bgpExpiryIPv6Header:]
		expiredSource, _ := netip.AddrFromSlice(expired[8:24])
		expiredDestination, _ := netip.AddrFromSlice(expired[24:40])
		port := int(segment[2])<<8 | int(segment[3])
		synchronize := segment[bgpExpiryTCPFlags]&bgpExpirySYN != 0
		if expired[bgpExpiryNextHeader] == tunnelRuntimeProtocolTCP && expiredSource == source && expiredDestination == destination &&
			port == bgpRuntimePort && synchronize {
			return true
		}
	}
}

func setBGPMultihopSecondNeighbor(t *testing.T, present bool) {
	t.Helper()
	link, err := netlink.LinkByName(tunnelRuntimeDevice)
	if err != nil {
		t.Fatal(err)
	}
	_, destination, err := net.ParseCIDR(bgpUpstreamAddress + "/128")
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: destination, Gw: net.ParseIP(bgpMultihopSecondNeighbor)}
	if present {
		err = netlink.RouteAdd(route)
	} else {
		err = netlink.RouteDel(route)
	}
	if err != nil {
		t.Fatalf("change the route to the upstream through %s: %v", bgpMultihopSecondNeighbor, err)
	}
}
