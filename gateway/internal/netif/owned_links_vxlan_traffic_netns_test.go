//go:build linux && netns

package netif

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/interfaceintent"
)

const (
	vxlanPeerUnderlay = "vx-peer"
	vxlanRequest      = "VXLAN request"
	vxlanReply        = "VXLAN reply"
)

func TestOwnedVXLANCarriesIPv6OverIPv4(t *testing.T) {
	if !enterVXLANNamespace(t) {
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	home, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer home.Close()
	defer func() {
		if err := netns.Set(home); err != nil {
			t.Error(err)
		}
	}()
	peerNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer peerNamespace.Close()
	if err := netns.Set(home); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: vxlanUnderlayName},
		PeerName:  vxlanPeerUnderlay, PeerNamespace: netlink.NsFd(int(peerNamespace)),
	}); err != nil {
		t.Fatal(err)
	}
	configureVXLANEndpoint(t, vxlanUnderlayName, tunnelLocalOuter, tunnelRemoteOuter, tunnelLocalInner)
	underlay, err := net.InterfaceByName(vxlanUnderlayName)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := packet.Listen(underlay, packet.Raw, unix.ETH_P_ALL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer capture.Close()
	deadline, bounded := t.Deadline()
	if !bounded {
		t.Fatal("VXLAN traffic capture requires a test deadline")
	}
	if err := capture.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := netns.Set(peerNamespace); err != nil {
		t.Fatal(err)
	}
	configureVXLANEndpoint(t, vxlanPeerUnderlay, tunnelRemoteOuter, tunnelLocalOuter, tunnelRemoteInner)
	peerUnderlay, err := net.InterfaceByName(vxlanPeerUnderlay)
	if err != nil {
		t.Fatal(err)
	}
	if err := netns.Set(home); err != nil {
		t.Fatal(err)
	}
	exchanges := [][2]netip.AddrPort{
		exchangeVXLAN(t, peerNamespace, tunnelRemoteInner, home, home),
		exchangeVXLAN(t, home, tunnelLocalInner, peerNamespace, home),
	}
	captureVXLANExchanges(t, capture, exchanges, underlay, peerUnderlay)
}

func captureVXLANExchanges(t *testing.T, capture *packet.Conn, exchanges [][2]netip.AddrPort, underlay, peerUnderlay *net.Interface) {
	t.Helper()
	seen := make([][2]bool, len(exchanges))
	remaining := len(exchanges) * 2
	buffer := make([]byte, 65536)
	for remaining != 0 {
		count, _, err := capture.ReadFrom(buffer)
		if err != nil {
			t.Fatalf("capture bidirectional VXLAN envelopes: remaining=%d error=%v", remaining, err)
		}
		decoded := gopacket.NewPacket(buffer[:count], layers.LayerTypeEthernet, gopacket.Default)
		encapsulation, present := decoded.Layer(layers.LayerTypeVXLAN).(*layers.VXLAN)
		if !present {
			continue
		}
		inner := gopacket.NewPacket(encapsulation.Payload, layers.LayerTypeEthernet, gopacket.Default)
		for index, endpoints := range exchanges {
			for direction := range 2 {
				if seen[index][direction] || !vxlanExchangePacket(inner, endpoints, direction) {
					continue
				}
				requireVXLANEnvelope(t, decoded, encapsulation, underlay, peerUnderlay)
				seen[index][direction] = true
				remaining--
			}
		}
	}
}

func configureVXLANEndpoint(t *testing.T, underlayName, local, remote, inner string) {
	t.Helper()
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
	// ECN negotiation changes outer ECN bits; this fixture compares a fixed TOS byte.
	if err := os.WriteFile("/proc/sys/net/ipv4/tcp_ecn", []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	underlay, err := netlink.LinkByName(underlayName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(underlay, mustTunnelAddress(t, local+"/30")); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(underlay); err != nil {
		t.Fatal(err)
	}
	connection := vxlanConnection()
	connection.Link.Tunnel.Underlay = underlayName
	connection.Link.Tunnel.Local = netip.MustParseAddr(local)
	connection.Link.Tunnel.Remote = netip.MustParseAddr(remote)
	reconciler := newTunnelReconciler(t, filepath.Join(t.TempDir(), "links.json"))
	requireTunnelReady(t, reconcileVXLAN(t, reconciler, connection, externalConnection(underlayName)))
	device := vxlanDevice(t, vxlanName)
	requireVXLANAttributes(t, device, connection, underlay)
	if err := netlink.AddrAdd(device, mustTunnelAddress(t, inner+"/64")); err != nil {
		t.Fatal(err)
	}
	if device.MTU+interfaceintent.TunnelVXLANOverhead != underlay.Attrs().MTU {
		t.Fatal("VXLAN MTU does not account for the complete envelope")
	}
}

func exchangeVXLAN(t *testing.T, serverNamespace netns.NsHandle, serverAddress string, clientNamespace, home netns.NsHandle) [2]netip.AddrPort {
	t.Helper()
	deadline, bounded := t.Deadline()
	if !bounded {
		t.Fatal("VXLAN TCP exchange requires a test deadline")
	}
	if err := netns.Set(serverNamespace); err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenTCP("tcp6", &net.TCPAddr{IP: net.ParseIP(serverAddress)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- serveVXLANExchange(listener, deadline) }()
	if err := netns.Set(clientNamespace); err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{Deadline: deadline}
	connection, dialErr := dialer.DialContext(t.Context(), "tcp6", listener.Addr().String())
	if err := netns.Set(home); err != nil {
		t.Fatal(err)
	}
	if dialErr != nil {
		t.Fatal(dialErr)
	}
	defer connection.Close()
	if err := connection.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte(vxlanRequest)); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, len(vxlanReply))
	if _, err := io.ReadFull(connection, reply); err != nil || string(reply) != vxlanReply {
		t.Fatalf("VXLAN TCP reply=%q error=%v", reply, err)
	}
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	source, err := netip.ParseAddrPort(connection.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	destination, err := netip.ParseAddrPort(connection.RemoteAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	return [2]netip.AddrPort{source, destination}
}

func serveVXLANExchange(listener *net.TCPListener, deadline time.Time) error {
	connection, err := listener.AcceptTCP()
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(deadline); err != nil {
		return err
	}
	request := make([]byte, len(vxlanRequest))
	if _, err := io.ReadFull(connection, request); err != nil {
		return err
	}
	if string(request) != vxlanRequest {
		return errors.New("VXLAN TCP request payload changed")
	}
	_, err = connection.Write([]byte(vxlanReply))
	return err
}

func vxlanExchangePacket(decoded gopacket.Packet, endpoints [2]netip.AddrPort, direction int) bool {
	ipv6, networkPresent := decoded.Layer(layers.LayerTypeIPv6).(*layers.IPv6)
	tcp, transportPresent := decoded.Layer(layers.LayerTypeTCP).(*layers.TCP)
	if !networkPresent || !transportPresent || ipv6.HopLimit != 64 {
		return false
	}
	source, destination := endpoints[0], endpoints[1]
	payload := vxlanRequest
	if direction == 1 {
		source, destination, payload = destination, source, vxlanReply
	}
	return ipv6.SrcIP.Equal(source.Addr().AsSlice()) && ipv6.DstIP.Equal(destination.Addr().AsSlice()) &&
		tcp.SrcPort == layers.TCPPort(source.Port()) && tcp.DstPort == layers.TCPPort(destination.Port()) && string(tcp.Payload) == payload
}

func requireVXLANEnvelope(t *testing.T, decoded gopacket.Packet, encapsulation *layers.VXLAN, home, peer *net.Interface) {
	t.Helper()
	ethernet, ethernetPresent := decoded.Layer(layers.LayerTypeEthernet).(*layers.Ethernet)
	ipv4, networkPresent := decoded.Layer(layers.LayerTypeIPv4).(*layers.IPv4)
	udp, transportPresent := decoded.Layer(layers.LayerTypeUDP).(*layers.UDP)
	if !ethernetPresent || !networkPresent || !transportPresent {
		t.Fatalf("VXLAN packet omitted the outer Ethernet/IPv4/UDP envelope: %v", decoded)
	}
	sourceMAC, destinationMAC := home.HardwareAddr, peer.HardwareAddr
	sourceIP, destinationIP := tunnelLocalOuter, tunnelRemoteOuter
	if ipv4.SrcIP.Equal(net.ParseIP(tunnelRemoteOuter)) {
		sourceMAC, destinationMAC, sourceIP, destinationIP = destinationMAC, sourceMAC, destinationIP, sourceIP
	}
	if !bytes.Equal(ethernet.SrcMAC, sourceMAC) || !bytes.Equal(ethernet.DstMAC, destinationMAC) ||
		!ipv4.SrcIP.Equal(net.ParseIP(sourceIP)) || !ipv4.DstIP.Equal(net.ParseIP(destinationIP)) ||
		ipv4.Version != 4 || ipv4.IHL != 5 || ipv4.Protocol != layers.IPProtocolUDP || ipv4.TTL != vxlanOuterTTL || ipv4.TOS != vxlanOuterTOS ||
		udp.SrcPort != vxlanSourcePort || udp.DstPort != vxlanDestinationPort ||
		encapsulation.VNI != 0 || !encapsulation.ValidIDFlag || !bytes.Equal(encapsulation.Contents, []byte{8, 0, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("VXLAN outer envelope differs from configured fields: Ethernet=%+v IPv4=%+v UDP=%+v VXLAN=%+v", ethernet, ipv4, udp, encapsulation)
	}
	if int(ipv4.Length) != len(ipv4.Contents)+len(ipv4.Payload) || int(udp.Length) != 8+len(udp.Payload) || vxlanIPv4Checksum(ipv4.Contents) != 0 {
		t.Fatalf("VXLAN envelope length or IPv4 checksum is invalid: IPv4=%+v UDP=%+v", ipv4, udp)
	}
	t.Logf("VXLAN %s:%d -> %s:%d VNI=%d TTL=%d TOS=%d inner=%d bytes", ipv4.SrcIP, udp.SrcPort, ipv4.DstIP, udp.DstPort, encapsulation.VNI, ipv4.TTL, ipv4.TOS, len(encapsulation.Payload))
}

func vxlanIPv4Checksum(header []byte) uint16 {
	var sum uint32
	for index := 0; index < len(header); index += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[index : index+2]))
	}
	for sum > 65535 {
		sum = sum&65535 + sum>>16
	}
	return ^uint16(sum)
}
