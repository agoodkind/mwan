//go:build linux && ovs

package commandline_test

import (
	"bytes"
	"fmt"
	"math/bits"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type packetTuple struct {
	source, destination netip.AddrPort
}

func (p packetTuple) reply() packetTuple { return packetTuple{p.destination, p.source} }

func sendReceiveUDP(t *testing.T, sender, receiver int, packet packetTuple, payload string) packetTuple {
	t.Helper()
	family := layers.EthernetTypeIPv4
	src, dst := packet.source.Addr().AsSlice(), packet.destination.Addr().AsSlice()
	var network interface {
		gopacket.NetworkLayer
		gopacket.SerializableLayer
	}
	if packet.source.Addr().Is4() {
		network = &layers.IPv4{Version: 4, TTL: 64, Protocol: layers.IPProtocolUDP, SrcIP: src, DstIP: dst}
	} else {
		family = layers.EthernetTypeIPv6
		network = &layers.IPv6{Version: 6, HopLimit: 64, NextHeader: layers.IPProtocolUDP, SrcIP: src, DstIP: dst}
	}
	send := ovsPacketSocket(t, sender, uint16(family))
	receive := ovsPacketSocket(t, receiver, uint16(family))
	ethernet := &layers.Ethernet{SrcMAC: net.HardwareAddr{2, 0, 0, 0, 0, 1}, DstMAC: net.HardwareAddr{2, 0, 0, 0, 0, 2}, EthernetType: family}
	udp := &layers.UDP{SrcPort: layers.UDPPort(packet.source.Port()), DstPort: layers.UDPPort(packet.destination.Port())}
	if err := udp.SetNetworkLayerForChecksum(network); err != nil {
		t.Fatal(err)
	}
	data := gopacket.NewSerializeBuffer()
	options := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(data, options, ethernet, network, udp, gopacket.Payload(payload)); err != nil {
		t.Fatal(err)
	}
	target := &unix.SockaddrLinklayer{Ifindex: sender, Protocol: bits.ReverseBytes16(uint16(family))}
	if err := unix.Sendto(send, data.Bytes(), 0, target); err != nil {
		t.Fatal(err)
	}
	poll := []unix.PollFd{{Fd: int32(receive), Events: unix.POLLIN}}
	var buffer [2048]byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if pollOVSPackets(t, poll, 100) == 0 {
			continue
		}
		length, _, err := unix.Recvfrom(receive, buffer[:], 0)
		if err != nil {
			t.Fatal(err)
		}
		frame := gopacket.NewPacket(buffer[:length], layers.LayerTypeEthernet, gopacket.Default)
		transport, ok := frame.TransportLayer().(*layers.UDP)
		if !ok || !bytes.Equal(transport.Payload, []byte(payload)) {
			continue
		}
		ips := frame.NetworkLayer().NetworkFlow()
		source, ok := netip.AddrFromSlice(ips.Src().Raw())
		if !ok {
			t.Fatalf("invalid captured source %v", ips.Src())
		}
		destination, ok := netip.AddrFromSlice(ips.Dst().Raw())
		if !ok {
			t.Fatalf("invalid captured destination %v", ips.Dst())
		}
		return packetTuple{netip.AddrPortFrom(source.Unmap(), uint16(transport.SrcPort)), netip.AddrPortFrom(destination.Unmap(), uint16(transport.DstPort))}
	}
	t.Fatalf("interface %d did not receive UDP payload %q from interface %d", receiver, payload, sender)
	return packetTuple{}
}

func TestOVSReadableTranslation(t *testing.T) {
	socket := os.Getenv("MWAN_TEST_OVS_SOCKET")
	if socket == "" {
		t.Fatal("run make test-ovs to start a disposable OVS instance")
	}
	ports := map[string]int{}
	for _, port := range []string{"vlan-l", "vlan-w", "v6-l", "v6-w", "map-l", "map-w", "nat-l", "nat-w"} {
		ports[port] = ovsPacketLink(t, "rp-"+port, "peer-"+port)
	}
	vlan := &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: "peer-rp-tag", ParentIndex: ports["vlan-w"]}, VlanId: 37}
	if err := netlink.LinkAdd(vlan); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(vlan); err != nil {
		t.Fatal(err)
	}
	vlanLink, err := netlink.LinkByName("peer-rp-tag")
	if err != nil {
		t.Fatal(err)
	}
	ports["tag"] = vlanLink.Attrs().Index
	document, err := os.ReadFile("testdata/ovs-readable-packets.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(socket)
	config := strings.ReplaceAll(string(document), "/run/openvswitch/", dir+"/")
	config = strings.Replace(config, filepath.Join(dir, "db.sock"), socket, 1)
	path := filepath.Join(t.TempDir(), "network.json")
	writeOVSPacketDocument(t, path, config)
	startOVSPacketService(t, path, "rp-vlan-l", time.Hour)
	bridge := "unix:" + filepath.Join(dir, "br-rp.mgmt")
	deadline := time.Now().Add(ovsReconcileWait)
	for {
		data, err := exec.CommandContext(t.Context(), "ovs-ofctl", "-O", "OpenFlow15", "dump-flows", bridge, "table=1").CombinedOutput()
		if err == nil && bytes.Contains(data, []byte("ct_state=")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MWAN did not install the connection tracking rules: %v: %s", err, data)
		}
		time.Sleep(50 * time.Millisecond)
	}
	server := netip.MustParseAddrPort("198.51.100.7:443")
	for _, tc := range []struct {
		name, lan, wan             string
		source, translated, server netip.AddrPort
	}{
		{"vlan", "vlan-l", "tag", netip.MustParseAddrPort("192.0.2.1:35001"), netip.MustParseAddrPort("192.0.2.1:35001"), server},
		{"static-ipv4", "map-l", "map-w", netip.MustParseAddrPort("192.0.2.41:35002"), netip.MustParseAddrPort("203.0.113.41:35002"), server},
		{"masked-ipv6", "v6-l", "v6-w", netip.MustParseAddrPort("[fd01:2345:6789:abcd:1234:5678:9abc:def0]:35003"), netip.MustParseAddrPort("[2001:db8:123:456:1234:5678:9abc:def0]:35003"), netip.MustParseAddrPort("[2001:db8:ffff::7]:443")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original := packetTuple{tc.source, tc.server}
			out := sendReceiveUDP(t, ports[tc.lan], ports[tc.wan], original, tc.name+"-out")
			if want := (packetTuple{tc.translated, tc.server}); out != want {
				t.Fatalf("outbound %v, want %v", out, want)
			}
			back := sendReceiveUDP(t, ports[tc.wan], ports[tc.lan], out.reply(), tc.name+"-reply")
			if back != original.reply() {
				t.Fatalf("return %v, want %v", back, original.reply())
			}
		})
	}
	t.Run("shared-ipv4", func(t *testing.T) {
		public := netip.MustParseAddr("203.0.113.61")
		mappings := map[netip.AddrPort]packetTuple{}
		for i, client := range []string{"192.0.2.61:39000", "192.0.2.62:39000"} {
			original := packetTuple{netip.MustParseAddrPort(client), server}
			out := sendReceiveUDP(t, ports["nat-l"], ports["nat-w"], original, fmt.Sprintf("shared-%d", i))
			if out.source.Addr() != public || out.destination != server {
				t.Fatalf("unexpected outbound tuple %v", out)
			}
			if _, exists := mappings[out.source]; exists {
				t.Fatalf("both clients used %v", out.source)
			}
			mappings[out.source] = original
		}
		for external, original := range mappings {
			back := sendReceiveUDP(t, ports["nat-w"], ports["nat-l"], packetTuple{server, external}, "reply-"+original.source.String())
			if back != original.reply() {
				t.Fatalf("reply for %v became %v; want %v", external, back, original.reply())
			}
		}
	})
}
