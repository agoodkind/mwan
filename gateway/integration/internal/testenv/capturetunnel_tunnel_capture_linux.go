//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

type TunnelRuntimeFrame struct {
	EtherType        uint16
	Length           int
	Protocol         uint8
	Source           netip.Addr
	Destination      netip.Addr
	InnerSource      netip.Addr
	InnerDestination netip.Addr
}

func OpenTunnelRuntimeCapture(t *testing.T, topology TunnelRuntimeTopology) int {
	t.Helper()
	return OpenTunnelRuntimeLinkCapture(t, topology, topology.Isp, TunnelRuntimeISPLink)
}

func OpenTunnelRuntimeLinkCapture(
	t *testing.T,
	topology TunnelRuntimeTopology,
	router netns.NsHandle,
	name string,
) int {
	t.Helper()
	SetRuntimeNamespace(t, router)
	defer SetRuntimeNamespace(t, topology.Gateway)
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
	if err := unix.Bind(
		fd,
		&unix.SockaddrLinklayer{Protocol: protocol, Ifindex: link.Attrs().Index},
	); err != nil {
		t.Fatalf("bind the ISP link capture: %v", err)
	}
	return fd
}

func ReadTunnelRuntimeCapture(t *testing.T, fd int) []TunnelRuntimeFrame {
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
	var frames []TunnelRuntimeFrame
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
		frame := TunnelRuntimeFrame{
			EtherType: uint16(buffer[12])<<8 | uint16(buffer[13]),
			Length:    length,
		}
		if frame.EtherType == TunnelRuntimeEtherIPv4 && captured >= ethernetHeader+ipv4Header {
			header := buffer[ethernetHeader:captured]
			frame.Protocol = header[9]
			frame.Source, _ = netip.AddrFromSlice(header[12:16])
			frame.Destination, _ = netip.AddrFromSlice(header[16:20])
			headerLength := int(header[0]&0x0f) * 4
			if frame.Protocol == TunnelRuntimeProtocol41 && len(header) >= headerLength+ipv6Header {
				inner := header[headerLength:]
				frame.InnerSource, _ = netip.AddrFromSlice(inner[8:24])
				frame.InnerDestination, _ = netip.AddrFromSlice(inner[24:40])
			}
		}
		frames = append(frames, frame)
	}
}
