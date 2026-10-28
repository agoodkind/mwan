//go:build linux && firewallnetns

package main

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

type tunnelRuntimeFrame struct {
	etherType        uint16
	length           int
	protocol         uint8
	source           netip.Addr
	destination      netip.Addr
	innerSource      netip.Addr
	innerDestination netip.Addr
}

func openTunnelRuntimeCapture(t *testing.T, topology tunnelRuntimeTopology) int {
	t.Helper()
	return openTunnelRuntimeLinkCapture(t, topology, topology.isp, tunnelRuntimeISPLink)
}

func openTunnelRuntimeLinkCapture(t *testing.T, topology tunnelRuntimeTopology, router netns.NsHandle, name string) int {
	t.Helper()
	setRuntimeNamespace(t, router)
	defer setRuntimeNamespace(t, topology.gateway)
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	protocol := uint16(unix.ETH_P_ALL<<8 | unix.ETH_P_ALL>>8)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK, int(protocol))
	if err != nil {
		t.Fatalf("open the ISP link capture: %v", err)
	}
	if err := unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, 64<<20); err != nil {
		t.Fatalf("size the ISP link capture: %v", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: protocol, Ifindex: link.Attrs().Index}); err != nil {
		t.Fatalf("bind the ISP link capture: %v", err)
	}
	return fd
}

func readTunnelRuntimeCapture(t *testing.T, fd int) []tunnelRuntimeFrame {
	t.Helper()
	defer func() { _ = unix.Close(fd) }()
	const ethernetHeader, ipv4Header, ipv6Header = 14, 20, 40
	statistics, err := unix.GetsockoptTpacketStats(fd, unix.SOL_PACKET, unix.PACKET_STATISTICS)
	if err != nil {
		t.Fatalf("read the capture statistics: %v", err)
	}
	if statistics.Drops != 0 {
		t.Fatalf("the ISP link capture dropped %d frames", statistics.Drops)
	}
	var frames []tunnelRuntimeFrame
	buffer := make([]byte, 128)
	for {
		length, _, err := unix.Recvfrom(fd, buffer, unix.MSG_TRUNC)
		if errors.Is(err, unix.EAGAIN) {
			return frames
		}
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.ENETDOWN) {
			continue
		}
		if err != nil {
			t.Fatalf("read the ISP link capture: %v", err)
		}
		captured := min(length, len(buffer))
		if captured < ethernetHeader {
			continue
		}
		frame := tunnelRuntimeFrame{etherType: uint16(buffer[12])<<8 | uint16(buffer[13]), length: length}
		if frame.etherType == tunnelRuntimeEtherIPv4 && captured >= ethernetHeader+ipv4Header {
			header := buffer[ethernetHeader:captured]
			frame.protocol = header[9]
			frame.source, _ = netip.AddrFromSlice(header[12:16])
			frame.destination, _ = netip.AddrFromSlice(header[16:20])
			headerLength := int(header[0]&0x0f) * 4
			if frame.protocol == tunnelRuntimeProtocol41 && len(header) >= headerLength+ipv6Header {
				inner := header[headerLength:]
				frame.innerSource, _ = netip.AddrFromSlice(inner[8:24])
				frame.innerDestination, _ = netip.AddrFromSlice(inner[24:40])
			}
		}
		frames = append(frames, frame)
	}
}
