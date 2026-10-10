//go:build linux && netns

package wanroutes

import (
	"bytes"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type outerFrame struct {
	destinationMAC net.HardwareAddr
	source         netip.Addr
	destination    netip.Addr
}

func openTunnelRouteCapture(t *testing.T, iface string) int {
	t.Helper()
	link, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	protocol := uint16(unix.ETH_P_ALL<<8 | unix.ETH_P_ALL>>8)
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(protocol))
	if err != nil {
		t.Fatalf("open capture on %s: %v", iface, err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Protocol: protocol, Ifindex: link.Attrs().Index}); err != nil {
		t.Fatalf("bind capture to %s: %v", iface, err)
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Usec: 50000}); err != nil {
		t.Fatal(err)
	}
	return fd
}

// The frame reader selects IPv4 protocol 41 frames. The frame reader returns after the first frame when
// first is true.
func readOuterFrames(t *testing.T, fd int, window time.Duration, first bool) []outerFrame {
	t.Helper()
	const ethernetHeader, ipv4Header = 14, 20
	var frames []outerFrame
	buffer := make([]byte, 2048)
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		count, _, err := unix.Recvfrom(fd, buffer, 0)
		if errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatalf("capture read: %v", err)
		}
		if count < ethernetHeader+ipv4Header || buffer[12] != 0x08 || buffer[13] != 0x00 || buffer[ethernetHeader+9] != ipProtocolIPv6InIPv4 {
			continue
		}
		source, _ := netip.AddrFromSlice(buffer[ethernetHeader+12 : ethernetHeader+16])
		destination, _ := netip.AddrFromSlice(buffer[ethernetHeader+16 : ethernetHeader+20])
		frames = append(frames, outerFrame{destinationMAC: bytes.Clone(buffer[:6]), source: source, destination: destination})
		if first {
			return frames
		}
	}
	return frames
}

func sendThroughTunnel(t *testing.T) {
	t.Helper()
	connection, err := net.DialTimeout("udp6", net.JoinHostPort(tunnelRouteInnerPeer, "9"), time.Second)
	if err != nil {
		t.Fatalf("dial through the tunnel: %v", err)
	}
	defer func() { _ = connection.Close() }()
	if _, err := connection.Write([]byte("tunnel-endpoint-route")); err != nil {
		t.Fatalf("send through the tunnel: %v", err)
	}
}
