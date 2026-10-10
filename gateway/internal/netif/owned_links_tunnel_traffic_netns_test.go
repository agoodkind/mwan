//go:build linux && netns

package netif

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

// outerPacket stores one IPv4 packet from an underlay capture and the packet's inner IPv6 addresses.
type outerPacket struct {
	source      string
	destination string
	protocol    int
	innerSource string
	innerTarget string
}

func captureOuterPackets(t *testing.T, capture *packet.Conn, complete func([]outerPacket) bool) []outerPacket {
	t.Helper()
	if err := capture.SetReadDeadline(time.Now().Add(tunnelCaptureWindow)); err != nil {
		t.Fatal(err)
	}
	var packets []outerPacket
	buffer := make([]byte, 65536)
	for !complete(packets) {
		length, _, err := capture.ReadFrom(buffer)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return packets
		}
		if err != nil {
			t.Fatalf("read captured packet: %v", err)
		}
		if length == 0 || buffer[0]>>4 != ipv4.Version {
			continue
		}
		outer, err := ipv4.ParseHeader(buffer[:length])
		if err != nil {
			t.Fatalf("parse captured IPv4 header: %v", err)
		}
		captured := outerPacket{source: outer.Src.String(), destination: outer.Dst.String(), protocol: outer.Protocol}
		if outer.Protocol == unix.IPPROTO_IPV6 {
			inner, err := ipv6.ParseHeader(buffer[outer.Len:length])
			if err != nil {
				t.Fatalf("parse encapsulated IPv6 header: %v", err)
			}
			captured.innerSource, captured.innerTarget = inner.Src.String(), inner.Dst.String()
		}
		packets = append(packets, captured)
	}
	return packets
}

func exchangeOverTunnel(t *testing.T, listenerNamespace netns.NsHandle, listenAddress string, dialerNamespace netns.NsHandle, home netns.NsHandle) {
	t.Helper()
	if err := netns.Set(listenerNamespace); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp6", net.JoinHostPort(listenAddress, "0"))
	if err != nil {
		t.Fatalf("listen on %s: %v", listenAddress, err)
	}
	defer listener.Close()
	served := make(chan error, 1)
	go func() {
		accepted, err := listener.Accept()
		if err != nil {
			served <- err
			return
		}
		defer accepted.Close()
		request := make([]byte, len("request"))
		if _, err := io.ReadFull(accepted, request); err != nil {
			served <- err
			return
		}
		_, err = accepted.Write([]byte("reply"))
		served <- err
	}()
	if err := netns.Set(dialerNamespace); err != nil {
		t.Fatal(err)
	}
	dialed, err := net.DialTimeout("tcp6", listener.Addr().String(), tunnelCaptureWindow)
	if restoreErr := netns.Set(home); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err != nil {
		t.Fatalf("dial %s through the tunnel: %v", listener.Addr(), err)
	}
	defer dialed.Close()
	if err := dialed.SetDeadline(time.Now().Add(tunnelCaptureWindow)); err != nil {
		t.Fatal(err)
	}
	if _, err := dialed.Write([]byte("request")); err != nil {
		t.Fatalf("send through the tunnel: %v", err)
	}
	reply := make([]byte, len("reply"))
	if _, err := io.ReadFull(dialed, reply); err != nil || string(reply) != "reply" {
		t.Fatalf("reply through the tunnel = %q, err = %v", reply, err)
	}
	if err := <-served; err != nil {
		t.Fatalf("serve through the tunnel: %v", err)
	}
}

func TestOwnedTunnelCarriesIPv6InsideIPv4(t *testing.T) {
	if !enterTunnelNamespace(t) {
		return
	}
	// The namespace switches below apply to the calling thread only.
	runtime.LockOSThread()
	home, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	peerNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := netns.Set(home); err != nil {
		t.Fatal(err)
	}
	veth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: tunnelUnderlayName}, PeerName: "tun-peer",
		PeerNamespace: netlink.NsFd(int(peerNamespace)),
	}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatal(err)
	}
	underlay, err := netlink.LinkByName(tunnelUnderlayName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(underlay, mustTunnelAddress(t, tunnelLocalOuter+"/30")); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(underlay); err != nil {
		t.Fatal(err)
	}
	peer, err := netlink.NewHandleAt(peerNamespace)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	peerUnderlay, err := peer.LinkByName("tun-peer")
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.AddrAdd(peerUnderlay, mustTunnelAddress(t, tunnelRemoteOuter+"/30")); err != nil {
		t.Fatal(err)
	}
	if err := peer.LinkSetUp(peerUnderlay); err != nil {
		t.Fatal(err)
	}
	peerAttrs := netlink.NewLinkAttrs()
	peerAttrs.Name = "peer6in4"
	peerTunnel := &netlink.Sittun{
		LinkAttrs: peerAttrs, Link: uint32(peerUnderlay.Attrs().Index), Proto: unix.IPPROTO_IPV6,
		Local: net.ParseIP(tunnelRemoteOuter), Remote: net.ParseIP(tunnelLocalOuter),
	}
	if err := peer.LinkAdd(peerTunnel); err != nil {
		t.Fatalf("create the peer sit device: %v", err)
	}
	peerDevice, err := peer.LinkByName("peer6in4")
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.AddrAdd(peerDevice, mustTunnelAddress(t, tunnelRemoteInner+"/64")); err != nil {
		t.Fatal(err)
	}
	if err := peer.LinkSetUp(peerDevice); err != nil {
		t.Fatal(err)
	}

	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	requireTunnelReady(t, reconcileTunnel(t, reconciler, externalConnection(tunnelUnderlayName),
		tunnelConnection(tunnelUnderlayName, tunnelRemoteOuter, new(uint8(64)), nil)))
	if err := netlink.AddrAdd(tunnelDevice(t, tunnelName), mustTunnelAddress(t, tunnelLocalInner+"/64")); err != nil {
		t.Fatal(err)
	}

	underlayInterface, err := net.InterfaceByName(tunnelUnderlayName)
	if err != nil {
		t.Fatal(err)
	}
	// The kernel delivers transmitted packets only to sockets that receive every protocol.
	capture, err := packet.Listen(underlayInterface, packet.Datagram, unix.ETH_P_ALL, nil)
	if err != nil {
		t.Fatalf("open the underlay capture: %v", err)
	}
	defer capture.Close()

	exchangeOverTunnel(t, peerNamespace, tunnelRemoteInner, home, home)
	exchangeOverTunnel(t, home, tunnelLocalInner, peerNamespace, home)

	sawOutbound := func(captured outerPacket) bool {
		return captured.source == tunnelLocalOuter && captured.destination == tunnelRemoteOuter &&
			captured.innerSource == tunnelLocalInner && captured.innerTarget == tunnelRemoteInner
	}
	sawInbound := func(captured outerPacket) bool {
		return captured.source == tunnelRemoteOuter && captured.destination == tunnelLocalOuter &&
			captured.innerSource == tunnelRemoteInner && captured.innerTarget == tunnelLocalInner
	}
	bothDirections := func(packets []outerPacket) bool {
		outbound, inbound := false, false
		for _, captured := range packets {
			outbound = outbound || sawOutbound(captured)
			inbound = inbound || sawInbound(captured)
		}
		return outbound && inbound
	}
	packets := captureOuterPackets(t, capture, bothDirections)
	if !bothDirections(packets) {
		t.Fatalf("underlay capture lacks encapsulated packets in both directions: %+v", packets)
	}
	for _, captured := range packets {
		if captured.protocol != unix.IPPROTO_IPV6 {
			t.Fatalf("underlay packet uses IPv4 protocol %d, want %d: %+v", captured.protocol, unix.IPPROTO_IPV6, captured)
		}
	}
}
