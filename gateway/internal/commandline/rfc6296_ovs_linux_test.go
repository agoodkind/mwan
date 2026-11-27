//go:build linux && ovs

package commandline_test

// A software OVS bridge applies IPv6 prefix translation to TCP and UDP frames.
//
//	LAN peer <-> OVS <-> trunk peer (WAN VLAN 101 or 102)
//
// Packet checks compare packets against independently checksummed frames in both
// directions. It checks the WAN selection, verifies that translation preserves
// the transport bytes, and tests that reserved interface identifiers do not pass.

import (
	"bytes"
	"fmt"
	"math/bits"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/ovs"
	"goodkind.io/mwan/gateway/networkload"
)

type nptEndpoint struct {
	index  int
	socket int
}

func openNPTEndpoint(t *testing.T, index int) nptEndpoint {
	t.Helper()
	return nptEndpoint{index: index, socket: ovsPacketSocket(t, index, unix.ETH_P_IPV6)}
}

func addNPTVLAN(t *testing.T, trunk, wan int) nptEndpoint {
	t.Helper()
	link := &netlink.Vlan{
		LinkAttrs: netlink.LinkAttrs{Name: fmt.Sprintf("npt-%d", wan), ParentIndex: trunk},
		VlanId:    100 + wan,
	}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	device, err := netlink.LinkByName(link.Name)
	if err != nil {
		t.Fatal(err)
	}
	return openNPTEndpoint(t, device.Attrs().Index)
}

func nptFrame(t *testing.T, tuple packetTuple, protocol layers.IPProtocol, payload string) []byte {
	t.Helper()
	ethernet := &layers.Ethernet{
		SrcMAC:       net.HardwareAddr{2, 0, 0, 0, 0, 1},
		DstMAC:       net.HardwareAddr{2, 0, 0, 0, 0, 2},
		EthernetType: layers.EthernetTypeIPv6,
	}
	ip := &layers.IPv6{
		Version:    6,
		HopLimit:   64,
		NextHeader: protocol,
		SrcIP:      tuple.source.Addr().AsSlice(),
		DstIP:      tuple.destination.Addr().AsSlice(),
	}
	var transport interface {
		gopacket.SerializableLayer
		SetNetworkLayerForChecksum(gopacket.NetworkLayer) error
	}
	switch protocol {
	case layers.IPProtocolTCP:
		transport = &layers.TCP{
			SrcPort: layers.TCPPort(tuple.source.Port()),
			DstPort: layers.TCPPort(tuple.destination.Port()),
			Seq:     12345,
			ACK:     true,
			Window:  4096,
		}
	case layers.IPProtocolUDP:
		transport = &layers.UDP{
			SrcPort: layers.UDPPort(tuple.source.Port()),
			DstPort: layers.UDPPort(tuple.destination.Port()),
		}
	default:
		t.Fatalf("unsupported test protocol %v", protocol)
	}
	if err := transport.SetNetworkLayerForChecksum(ip); err != nil {
		t.Fatal(err)
	}
	buffer := gopacket.NewSerializeBuffer()
	options := gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}
	if err := gopacket.SerializeLayers(
		buffer,
		options,
		ethernet,
		ip,
		transport,
		gopacket.Payload(payload),
	); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func exchangeNPTFrame(
	t *testing.T,
	from nptEndpoint,
	to []nptEndpoint,
	frame []byte,
	payload string,
) (int, []byte) {
	t.Helper()
	sendNPTFrame(t, from, frame)
	wan, packet := receiveNPTFrame(t, to, payload, 2*time.Second)
	if packet == nil {
		t.Fatalf("did not receive packet %s from interface %d", payload, from.index)
	}
	return wan, packet
}

func sendNPTFrame(t *testing.T, from nptEndpoint, frame []byte) {
	t.Helper()
	address := &unix.SockaddrLinklayer{
		Ifindex:  from.index,
		Protocol: bits.ReverseBytes16(unix.ETH_P_IPV6),
	}
	if err := unix.Sendto(from.socket, frame, 0, address); err != nil {
		t.Fatal(err)
	}
}

func receiveNPTFrame(t *testing.T, to []nptEndpoint, payload string, timeout time.Duration) (
	int,
	[]byte,
) {
	t.Helper()
	pending := make([]unix.PollFd, 0, len(to))
	for _, endpoint := range to {
		pending = append(pending, unix.PollFd{Fd: int32(endpoint.socket), Events: unix.POLLIN})
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pollOVSPackets(t, pending, 100) == 0 {
			continue
		}
		for index, socket := range pending {
			if socket.Revents&unix.POLLIN == 0 {
				continue
			}
			var data [2048]byte
			n, _, err := unix.Recvfrom(int(socket.Fd), data[:], 0)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.HasSuffix(data[:n], []byte(payload)) {
				return index, data[:n]
			}
		}
	}
	return 0, nil
}

func checkNPTFrame(t *testing.T, input, received, expected []byte) {
	t.Helper()
	const ethernetLength = 14
	const transportOffset = ethernetLength + 40
	if len(
		received,
	) < transportOffset || !bytes.Equal(received[ethernetLength:], expected[ethernetLength:]) {
		t.Fatalf(
			"captured IPv6 packet differs from the independently checksummed packet\nreceived %x\nexpected %x",
			received,
			expected,
		)
	}
	if !bytes.Equal(input[transportOffset:], received[transportOffset:]) {
		t.Fatalf(
			"NPT changed transport header or payload\ninput %x\ncaptured %x",
			input[transportOffset:],
			received[transportOffset:],
		)
	}
}

func TestOVSReadableConnectionNPTv6(t *testing.T) {
	socket := os.Getenv("MWAN_TEST_OVS_SOCKET")
	if socket == "" {
		t.Fatal("run make test-ovs to start a disposable OVS instance")
	}
	lan := openNPTEndpoint(t, ovsPacketLink(t, "npt-lan", "npt-client"))
	trunk := ovsPacketLink(t, "npt-trunk", "npt-peer")
	wans := []nptEndpoint{addNPTVLAN(t, trunk, 1), addNPTVLAN(t, trunk, 2)}
	document, err := os.ReadFile("testdata/ovs-connection-npt.json")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(socket)
	path := filepath.Join(t.TempDir(), "network.json")
	config := strings.ReplaceAll(string(document), "/run/openvswitch/", dir+"/")
	writeOVSPacketDocument(
		t,
		path,
		strings.Replace(config, filepath.Join(dir, "db.sock"), socket, 1),
	)
	settings, err := networkload.LoadOVS(path, "../../yangpub/schema")
	if err != nil {
		t.Fatal(err)
	}
	if err := ovs.Setup(t.Context(), settings, nil); err != nil {
		t.Fatal(err)
	}

	// RFC 6296 section 3.6 gives the first WAN mapping.
	internal := netip.MustParseAddr("fd01:203:405:1::1234")
	external := []netip.Addr{
		netip.MustParseAddr("2001:db8:1:d550::1234"),
		netip.MustParseAddr("2001:db8:2:ab01:2a4e::1234"),
	}
	roundTrip := func(protocol layers.IPProtocol, clientPort, serverPort uint16) int {
		t.Helper()
		server := netip.MustParseAddr(fmt.Sprintf("2001:db8:ffff::%x", clientPort))
		original := packetTuple{
			source:      netip.AddrPortFrom(internal, clientPort),
			destination: netip.AddrPortFrom(server, serverPort),
		}
		payload := fmt.Sprintf("npt-%s-%d-%d-request", protocol, clientPort, serverPort)
		input := nptFrame(t, original, protocol, payload)
		wan, received := exchangeNPTFrame(t, lan, wans, input, payload)
		public := packetTuple{netip.AddrPortFrom(external[wan], clientPort), original.destination}
		checkNPTFrame(t, input, received, nptFrame(t, public, protocol, payload))
		payload += "-reply"
		reply := nptFrame(t, public.reply(), protocol, payload)
		_, received = exchangeNPTFrame(t, wans[wan], []nptEndpoint{lan}, reply, payload)
		checkNPTFrame(t, reply, received, nptFrame(t, original.reply(), protocol, payload))
		return wan
	}
	if wan := roundTrip(layers.IPProtocolTCP, 31000, 8443); wan != 1 {
		t.Fatalf("fixed flow selected VLAN %d, want 102", 101+wan)
	}
	for _, protocol := range []layers.IPProtocol{layers.IPProtocolUDP, layers.IPProtocolTCP} {
		seen := map[int]bool{}
		for port := uint16(32000); port < 32128 && len(seen) < len(wans); port++ {
			wan := roundTrip(protocol, port, 443)
			if !seen[wan] {
				if next := roundTrip(protocol, port, 443); next != wan {
					t.Fatalf(
						"SELECT changed WAN for the same %s flow from %d to %d",
						protocol,
						wan,
						next,
					)
				}
				seen[wan] = true
			}
		}
		if len(seen) != len(wans) {
			t.Fatalf("SELECT group forwarded %s through WAN indices %v", protocol, seen)
		}
	}
	internal = netip.MustParseAddr("fd01:203:405:1:ffff:1234:ffff:aaaa")
	external[1] = netip.MustParseAddr("2001:db8:2:ab01:ffff:3c82:ffff:aaaa")
	if wan := roundTrip(layers.IPProtocolTCP, 33000, 8443); wan != 1 {
		t.Fatalf("long-prefix test selected VLAN %d, want 102", 101+wan)
	}
	for _, source := range []string{"fd01:203:405:1:ffff:ffff:ffff:ffff", "fd01:203:405:1::"} {
		t.Run(
			source,
			func(t *testing.T) {
				payload := "reserved-iid-" + source
				tuple := packetTuple{
					source:      netip.AddrPortFrom(netip.MustParseAddr(source), 33000),
					destination: netip.MustParseAddrPort("[2001:db8:ffff::7]:8443"),
				}
				sendNPTFrame(t, lan, nptFrame(t, tuple, layers.IPProtocolTCP, payload))
				if wan, packet := receiveNPTFrame(
					t,
					wans,
					payload,
					200*time.Millisecond,
				); packet != nil {
					t.Fatalf(
						"OVS forwarded reserved IID %s on VLAN %d: %x",
						source,
						101+wan,
						packet,
					)
				}
			},
		)
	}
}
