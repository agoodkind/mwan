//go:build linux && netns

package bpf

import (
	"bytes"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"runtime"
	"testing"

	"github.com/cilium/ebpf"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestKernelTranslation(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	original, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	namespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer namespace.Close()
	defer func() {
		if err := netns.Set(original); err != nil {
			t.Error(err)
		}
	}()
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	lan, client := testVeth(t, "lan", "client")
	wan, internet := testVeth(t, "wan", "internet")
	pair := PrefixPair{ID: 7, Internal: netip.MustParsePrefix("fd01:203:405::/48"), External: netip.MustParsePrefix("2001:db8:1::/48")}
	internal := netip.MustParseAddr("fd01:203:405:1::1234")
	external := netip.MustParseAddr("2001:db8:1:d550::1234")
	remote := netip.MustParseAddr("2001:db8:ffff::10")
	secondInternal := netip.MustParseAddr("fd01:203:405:2::5678")
	secondExternal := netip.MustParseAddr("2001:db8:1:d551::5678")
	testRoute(t, lan, client, pair.Internal, internal, secondInternal)
	testRoute(t, wan, internet, netip.MustParsePrefix("2001:db8:ffff::/64"), remote)
	translator, err := New()
	if err != nil {
		var verifier *ebpf.VerifierError
		if errors.As(err, &verifier) {
			t.Fatalf("load programs: %+v", verifier)
		}
		t.Fatal(err)
	}
	defer func() { translator.Close() }()
	policies := []InterfacePolicy{{IfIndex: wan.Attrs().Index, Pairs: []PrefixPair{pair}}, {IfIndex: lan.Attrs().Index, Internal: true, Pairs: []PrefixPair{pair}}}
	states, err := translator.Reconcile(policies)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 4 {
		t.Fatalf("states: %+v", states)
	}
	for _, state := range states {
		if !state.Ready {
			t.Fatalf("not ready: %+v", state)
		}
	}
	clientSocket := testPacketSocket(t, client)
	internetSocket := testPacketSocket(t, internet)
	inNamespace(t, namespace, "wan-egress", func(t *testing.T) {
		testExchange(t, clientSocket, internetSocket, client, lan, internal, remote, external, remote)
	})
	inNamespace(t, namespace, "wan-ingress", func(t *testing.T) {
		testExchange(t, internetSocket, clientSocket, internet, wan, remote, external, remote, internal)
	})
	inNamespace(t, namespace, "tcp-wan-exchange", func(t *testing.T) {
		testTCPExchange(t, clientSocket, internetSocket, client, lan, internal, remote, external, remote)
		testTCPExchange(t, internetSocket, clientSocket, internet, wan, remote, external, remote, internal)
	})
	inNamespace(t, namespace, "hairpin", func(t *testing.T) {
		testExchange(t, clientSocket, clientSocket, client, lan, internal, secondExternal, external, secondInternal)
	})

	inNamespace(t, namespace, "unequal-prefix", func(t *testing.T) {
		unequal := pair
		unequal.External = netip.MustParsePrefix("2001:db8:1:aa00::/56")
		policies[0].Pairs = []PrefixPair{unequal}
		policies[1].Pairs = []PrefixPair{unequal}
		if _, err := translator.Reconcile(policies); err != nil {
			t.Fatal(err)
		}
		want := netip.MustParseAddr("2001:db8:1:aa01:2b4f::1234")
		testExchange(t, clientSocket, internetSocket, client, lan, internal, remote, want, remote)
		testExchange(t, internetSocket, clientSocket, internet, wan, remote, want, remote, internal)
		policies[0].Pairs = []PrefixPair{pair}
		policies[1].Pairs = []PrefixPair{pair}
		if _, err := translator.Reconcile(policies); err != nil {
			t.Fatal(err)
		}
	})
	inNamespace(t, namespace, "icmp-packet-too-big", func(t *testing.T) {
		testICMPError(t, internetSocket, clientSocket, internet, wan, remote, external, internal)
	})

	inNamespace(t, namespace, "hairpin-selects-second-prefix", func(t *testing.T) {
		second := pair
		second.ID = 8
		second.External = netip.MustParsePrefix("2001:db8:2::/48")
		policies[1].Pairs = []PrefixPair{pair, second}
		if _, err := translator.Reconcile(policies); err != nil {
			t.Fatal(err)
		}
		target := netip.MustParseAddr("2001:db8:2:d550::5678")
		wantSource := netip.MustParseAddr("2001:db8:2:d54f::1234")
		testExchange(t, clientSocket, clientSocket, client, lan, internal, target, wantSource, secondInternal)
		policies[1].Pairs = []PrefixPair{pair}
		if _, err := translator.Reconcile(policies); err != nil {
			t.Fatal(err)
		}
	})
	inNamespace(t, namespace, "restart-replaces-owned-attachments", func(t *testing.T) {
		if err := translator.Close(); err != nil {
			t.Fatal(err)
		}
		testExchange(t, clientSocket, internetSocket, client, lan, internal, remote, external, remote)
		translator, err = New()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := translator.Reconcile(policies); err != nil {
			t.Fatal(err)
		}
		testExchange(t, clientSocket, internetSocket, client, lan, internal, remote, external, remote)
	})

	inNamespace(t, namespace, "interface-replacement-preserves-old-on-failure", func(t *testing.T) {
		replacement, replacementPeer := testVeth(t, "replacement", "replacementpeer")
		if err := ensureClsact(replacement); err != nil {
			t.Fatal(err)
		}
		conflict := &netlink.BpfFilter{
			FilterAttrs: netlink.FilterAttrs{LinkIndex: replacement.Attrs().Index, Parent: netlink.HANDLE_MIN_EGRESS, Handle: filterHandle, Priority: filterPriority, Protocol: unix.ETH_P_ALL},
			Fd:          translator.objects.NptEgress.FD(), Name: "another-owner", DirectAction: true,
		}
		if err := netlink.FilterAdd(conflict); err != nil {
			t.Fatal(err)
		}
		replacementPolicies := []InterfacePolicy{{IfIndex: replacement.Attrs().Index, Pairs: []PrefixPair{pair}}, policies[1]}
		if _, err := translator.Reconcile(replacementPolicies); err == nil {
			t.Fatal("conflicting attachment unexpectedly succeeded")
		}
		for _, direction := range []Direction{Ingress, Egress} {
			program := translator.objects.NptIngress
			if direction == Egress {
				program = translator.objects.NptEgress
			}
			if err := readFilter(wan, direction, program); err != nil {
				t.Fatalf("old attachment was removed: %v", err)
			}
		}
		testExchange(t, clientSocket, internetSocket, client, lan, internal, remote, external, remote)
		if err := netlink.FilterDel(conflict); err != nil {
			t.Fatal(err)
		}
		if _, err := translator.Reconcile(replacementPolicies); err != nil {
			t.Fatal(err)
		}
		for _, direction := range []Direction{Ingress, Egress} {
			filters, err := netlink.FilterList(wan, filterParent(direction))
			if err != nil {
				t.Fatal(err)
			}
			for _, filter := range filters {
				if ownedFilter(filter, direction) {
					t.Fatal("old attachment remains after successful replacement")
				}
			}
		}
		testRoute(t, replacement, replacementPeer, netip.MustParsePrefix("2001:db8:ffff::/64"), remote)
		replacementSocket := testPacketSocket(t, replacementPeer)
		testExchange(t, clientSocket, replacementSocket, client, lan, internal, remote, external, remote)
		if _, err := translator.Reconcile(policies); err != nil {
			t.Fatal(err)
		}
		testRoute(t, wan, internet, netip.MustParsePrefix("2001:db8:ffff::/64"), remote)
	})
	inNamespace(t, namespace, "stateful-exceptions", func(t *testing.T) {
		excepted := pair
		excepted.SourceExceptions = []netip.Addr{internal}
		policies[0].Pairs = []PrefixPair{excepted}
		if _, err := translator.Reconcile(policies); err != nil {
			t.Fatal(err)
		}
		testExchange(t, clientSocket, internetSocket, client, lan, internal, remote, internal, remote)
	})
	inNamespace(t, namespace, "native-removal", func(t *testing.T) {
		if _, err := translator.Reconcile(nil); err != nil {
			t.Fatal(err)
		}
		testExchange(t, clientSocket, internetSocket, client, lan, internal, remote, internal, remote)
	})
}

func testVeth(t *testing.T, name, peerName string) (netlink.Link, netlink.Link) {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: peerName}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []netlink.Link{link, peer} {
		if err := netlink.LinkSetUp(item); err != nil {
			t.Fatal(err)
		}
	}
	return link, peer
}

func testRoute(t *testing.T, link, peer netlink.Link, prefix netip.Prefix, addresses ...netip.Addr) {
	t.Helper()
	_, network, err := net.ParseCIDR(prefix.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteReplace(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: network}); err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: link.Attrs().Index, IP: net.IP(address.AsSlice()), HardwareAddr: peer.Attrs().HardwareAddr, State: netlink.NUD_PERMANENT}); err != nil {
			t.Fatal(err)
		}
	}
}

func testPacketSocket(t *testing.T, link netlink.Link) int {
	t.Helper()
	fd, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(networkShort(unix.ETH_P_IPV6)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(fd) })
	if err := unix.Bind(fd, &unix.SockaddrLinklayer{Ifindex: link.Attrs().Index, Protocol: networkShort(unix.ETH_P_IPV6)}); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2}); err != nil {
		t.Fatal(err)
	}
	return fd
}

func networkShort(value uint16) uint16 { return value>>8 | value<<8 }

func testExchange(t *testing.T, send, receive int, sourceLink, destinationLink netlink.Link, source, destination, wantSource, wantDestination netip.Addr) {
	t.Helper()
	frame := make([]byte, 14+40+8+8)
	copy(frame[:6], destinationLink.Attrs().HardwareAddr)
	copy(frame[6:12], sourceLink.Attrs().HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], unix.ETH_P_IPV6)
	frame[14] = 0x60
	binary.BigEndian.PutUint16(frame[18:20], 16)
	frame[20], frame[21] = 17, 64
	copy(frame[22:38], source.AsSlice())
	copy(frame[38:54], destination.AsSlice())
	binary.BigEndian.PutUint16(frame[54:56], 12345)
	binary.BigEndian.PutUint16(frame[56:58], 23456)
	binary.BigEndian.PutUint16(frame[58:60], 16)
	copy(frame[62:], []byte("NPT-test"))
	binary.BigEndian.PutUint16(frame[60:62], packetChecksum(frame))
	if err := unix.Sendto(send, frame, 0, &unix.SockaddrLinklayer{Ifindex: sourceLink.Attrs().Index, Protocol: networkShort(unix.ETH_P_IPV6)}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2048)
	for {
		count, _, err := unix.Recvfrom(receive, data, 0)
		if err != nil {
			t.Fatalf("receive %s -> %s: %v", wantSource, wantDestination, err)
		}
		if count != len(frame) || !bytes.Equal(data[62:count], []byte("NPT-test")) {
			continue
		}
		actualSource := netip.AddrFrom16([16]byte(data[22:38]))
		actualDestination := netip.AddrFrom16([16]byte(data[38:54]))
		if data[21] != 63 {
			continue
		}
		if actualSource != wantSource || actualDestination != wantDestination {
			t.Fatalf("packet addresses = %s -> %s, want %s -> %s", actualSource, actualDestination, wantSource, wantDestination)
		}
		if packetChecksum(data[:count]) != 0 {
			t.Fatal("translation changed the UDP checksum sum")
		}
		return
	}
}

func testTCPExchange(t *testing.T, send, receive int, sourceLink, destinationLink netlink.Link, source, destination, wantSource, wantDestination netip.Addr) {
	t.Helper()
	packet := []byte("NPT-TCP!")
	frame := make([]byte, 14+40+20+len(packet))
	copy(frame[:6], destinationLink.Attrs().HardwareAddr)
	copy(frame[6:12], sourceLink.Attrs().HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], unix.ETH_P_IPV6)
	frame[14] = 0x60
	binary.BigEndian.PutUint16(frame[18:20], uint16(20+len(packet)))
	frame[20], frame[21] = 6, 64
	copy(frame[22:38], source.AsSlice())
	copy(frame[38:54], destination.AsSlice())
	binary.BigEndian.PutUint16(frame[54:56], 12345)
	binary.BigEndian.PutUint16(frame[56:58], 23456)
	binary.BigEndian.PutUint32(frame[58:62], 100)
	binary.BigEndian.PutUint32(frame[62:66], 200)
	frame[66], frame[67] = 0x50, 0x18
	binary.BigEndian.PutUint16(frame[68:70], 4096)
	copy(frame[74:], packet)
	binary.BigEndian.PutUint16(frame[70:72], packetChecksum(frame))
	if err := unix.Sendto(send, frame, 0, &unix.SockaddrLinklayer{Ifindex: sourceLink.Attrs().Index, Protocol: networkShort(unix.ETH_P_IPV6)}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2048)
	for {
		count, _, err := unix.Recvfrom(receive, data, 0)
		if err != nil {
			t.Fatalf("receive TCP %s -> %s: %v", wantSource, wantDestination, err)
		}
		if count != len(frame) || data[20] != 6 || !bytes.Equal(data[74:count], packet) {
			continue
		}
		actualSource := netip.AddrFrom16([16]byte(data[22:38]))
		actualDestination := netip.AddrFrom16([16]byte(data[38:54]))
		if data[21] != 63 || actualSource != wantSource || actualDestination != wantDestination {
			t.Fatalf("TCP packet = %s -> %s, hop limit %d; want %s -> %s, hop limit 63", actualSource, actualDestination, data[21], wantSource, wantDestination)
		}
		if packetChecksum(data[:count]) != 0 {
			t.Fatal("translation changed the TCP checksum sum")
		}
		return
	}
}

func packetChecksum(frame []byte) uint16 {
	var sum uint32
	for i := 22; i < 54; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(frame[i : i+2]))
	}
	sum += uint32(len(frame)-54) + uint32(frame[20])
	for i := 54; i < len(frame); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(frame[i : i+2]))
	}
	return ^fold(sum)
}

func inNamespace(t *testing.T, namespace netns.NsHandle, name string, test func(*testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		previous, err := netns.Get()
		if err != nil {
			t.Fatal(err)
		}
		defer previous.Close()
		if err := netns.Set(namespace); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := netns.Set(previous); err != nil {
				t.Error(err)
			}
		}()
		test(t)
	})
}

func testICMPError(t *testing.T, send, receive int, sourceLink, destinationLink netlink.Link, remote, external, internal netip.Addr) {
	t.Helper()
	frame := make([]byte, 14+40+8+40+8)
	copy(frame[:6], destinationLink.Attrs().HardwareAddr)
	copy(frame[6:12], sourceLink.Attrs().HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], unix.ETH_P_IPV6)
	frame[14], frame[20], frame[21] = 0x60, 58, 64
	binary.BigEndian.PutUint16(frame[18:20], uint16(len(frame)-54))
	copy(frame[22:38], remote.AsSlice())
	copy(frame[38:54], external.AsSlice())
	frame[54] = 2
	binary.BigEndian.PutUint32(frame[58:62], 1280)
	frame[62], frame[68], frame[69] = 0x60, 17, 63
	binary.BigEndian.PutUint16(frame[66:68], 8)
	copy(frame[70:86], external.AsSlice())
	copy(frame[86:102], remote.AsSlice())
	binary.BigEndian.PutUint16(frame[102:104], 12345)
	binary.BigEndian.PutUint16(frame[104:106], 23456)
	binary.BigEndian.PutUint16(frame[106:108], 8)
	binary.BigEndian.PutUint16(frame[56:58], packetChecksum(frame))
	if err := unix.Sendto(send, frame, 0, &unix.SockaddrLinklayer{Ifindex: sourceLink.Attrs().Index, Protocol: networkShort(unix.ETH_P_IPV6)}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2048)
	for {
		count, _, err := unix.Recvfrom(receive, data, 0)
		if err != nil {
			t.Fatal(err)
		}
		if count != len(frame) || data[54] != 2 {
			continue
		}
		if !bytes.Equal(data[38:54], internal.AsSlice()) || !bytes.Equal(data[70:86], internal.AsSlice()) {
			t.Fatalf("ICMPv6 addresses were not translated: %x", data[:count])
		}
		if packetChecksum(data[:count]) != 0 {
			t.Fatal("ICMPv6 checksum changed")
		}
		return
	}
}
