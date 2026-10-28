//go:build linux && netns

package steering

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/ti-mo/conntrack"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/offload"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	markProofFirstMark  = 73
	markProofSecondMark = 74
	markProofTable      = 420
	markProofPriority   = 100
	markProofSourcePort = 23456
	markProofDestPort   = 443
	markProofFlowExpiry = 120
	markProofAddressEnd = math.MaxUint16 - 1
)

func TestSteeringMapRoutesBothMarks(t *testing.T) {
	for _, mode := range []hashMode{hashModeRandom, hashModeSource, hashModeSourceDestination} {
		for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
			t.Run(string(mode)+"/"+strconv.Itoa(family), func(t *testing.T) {
				testSteeringMapRoutes(t, mode, family)
			})
		}
	}
}

func testSteeringMapRoutes(t *testing.T, mode hashMode, family int) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previous, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer previous.Close()
	current, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()
	defer func() {
		if err := netns.Set(previous); err != nil {
			t.Error(err)
		}
	}()
	deadline, present := t.Deadline()
	if !present {
		t.Fatal("steering packet proof requires a test deadline")
	}
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	for path, value := range map[string]string{
		"/proc/sys/net/ipv4/conf/all/rp_filter": "0", "/proc/sys/net/ipv4/conf/default/rp_filter": "0",
		"/proc/sys/net/ipv4/ip_forward": "1", "/proc/sys/net/ipv6/conf/all/forwarding": "1",
	} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	input, sender := hairpinVeth(t, "lan", "client")
	first, firstPeer := hairpinVeth(t, "wan0", "peer0")
	second, secondPeer := hairpinVeth(t, "wan1", "peer1")
	destination := netip.MustParseAddr("198.51.100.8")
	if family == unix.AF_INET6 {
		destination = netip.MustParseAddr("2001:db8:2::8")
	}
	markProofRoutes(t, family, destination, []netlink.Link{first, second}, []netlink.Link{firstPeer, secondPeer})
	markProofSaveMark(t)
	policy := markProofPolicy(t, mode, input, first, second)
	store := wanstate.New()
	store.SetRouting(0, map[string]wanstate.MemberRouting{
		"wan0": {Carrying: true, SelectionEnabled: true, V4Ready: true, V6Ready: true},
		"wan1": {Carrying: true, SelectionEnabled: true, V4Ready: true, V6Ready: true},
	})
	translation := wanstate.MemberTranslation{
		V4: wanstate.FamilyTranslation{Mode: "native", Ready: true},
		V6: wanstate.FamilyTranslation{Mode: "native", Ready: true},
	}
	store.SetTranslation(map[string]wanstate.MemberTranslation{"wan0": translation, "wan1": translation})
	writer, err := New(policy)
	if err != nil {
		t.Fatal(err)
	}
	monitorContext, stop := context.WithCancel(ctx)
	err = writer.Init(monitorContext, &ifmgr.Env{Log: slog.Default(), LiveState: store})
	stop()
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Reconcile(ctx, slog.Default()); err != nil {
		t.Fatal(err)
	}
	markProofPackets(t, ctx, input, sender, []netlink.Link{firstPeer, secondPeer}, destination)
	markProofReadback(t, ctx, writer, policy, store.Snapshot())
}

func markProofPolicy(t *testing.T, mode hashMode, input, first, second netlink.Link) Config {
	t.Helper()
	healthPath := filepath.Join(t.TempDir(), "health.state")
	if err := os.WriteFile(healthPath, []byte("wan0:healthy\nwan1:healthy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return Config{
		InternalIface: input.Attrs().Name, InternalNetV4: "10.0.0.0/16",
		InternalPrefix: "fd00::/64", OpnsenseEdgeV6: "fd00::1", HashMode: string(mode), HealthStateFile: healthPath,
		Members: []Member{
			{WANRef: ifmgr.WANRef{Name: "wan0", Iface: first.Attrs().Name}, Mark: markProofFirstMark, Weight: 1},
			{WANRef: ifmgr.WANRef{Name: "wan1", Iface: second.Attrs().Name}, Mark: markProofSecondMark, Weight: 1},
		},
	}
}

func markProofRoutes(t *testing.T, family int, destination netip.Addr, providers, peers []netlink.Link) {
	t.Helper()
	defaultRoute := netip.PrefixFrom(destination, 0).Masked()
	for index, provider := range providers {
		route := netlink.Route{
			Family: family, Table: markProofTable + index, LinkIndex: provider.Attrs().Index,
			Dst: &net.IPNet{IP: defaultRoute.Addr().AsSlice(), Mask: net.CIDRMask(0, destination.BitLen())},
		}
		if err := netlink.RouteAdd(&route); err != nil {
			t.Fatalf("install packet proof route family=%d table=%d interface=%s destination=%s: %v", family, route.Table, provider.Attrs().Name, route.Dst, err)
		}
		rule := netlink.NewRule()
		rule.Family, rule.Priority, rule.Table = family, markProofPriority+index, route.Table
		rule.Mark = []uint32{markProofFirstMark, markProofSecondMark}[index]
		if err := netlink.RuleAdd(rule); err != nil {
			t.Fatalf("install packet proof rule family=%d table=%d mark=%d: %v", family, rule.Table, rule.Mark, err)
		}
		if err := netlink.NeighSet(&netlink.Neigh{
			LinkIndex: provider.Attrs().Index, Family: family, IP: destination.AsSlice(),
			HardwareAddr: peers[index].Attrs().HardwareAddr, State: unix.NUD_PERMANENT,
		}); err != nil {
			t.Fatalf("install packet proof neighbor family=%d interface=%s destination=%s: %v", family, provider.Attrs().Name, destination, err)
		}
	}
}

func markProofSaveMark(t *testing.T) {
	t.Helper()
	connection, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	table := connection.AddTable(&nftables.Table{Name: "mark-proof", Family: nftables.TableFamilyINet})
	priority, policy := nftables.ChainPriorityMangle, nftables.ChainPolicyAccept
	chain := connection.AddChain(&nftables.Chain{
		Name: "postrouting", Table: table, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookPostrouting, Priority: priority, Policy: &policy,
	})
	connection.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyMARK, Register: 1},
		&expr.Ct{Key: expr.CtKeyMARK, Register: 1, SourceRegister: true},
	}})
	if err := connection.Flush(); err != nil {
		t.Fatal(err)
	}
}

func markProofPackets(t *testing.T, ctx context.Context, input, sender netlink.Link, peers []netlink.Link, destination netip.Addr) {
	t.Helper()
	protocol := uint16(unix.ETH_P_IP)
	if destination.Is6() {
		protocol = unix.ETH_P_IPV6
	}
	poll := make([]unix.PollFd, len(peers))
	for index, peer := range peers {
		socket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, int(hairpinNetworkShort(protocol)))
		if err != nil {
			t.Fatal(err)
		}
		if socket > math.MaxInt32 {
			unix.Close(socket)
			t.Fatal("packet socket descriptor exceeds poll width")
		}
		t.Cleanup(func() { unix.Close(socket) })
		if err := unix.Bind(socket, &unix.SockaddrLinklayer{Ifindex: peer.Attrs().Index, Protocol: hairpinNetworkShort(protocol)}); err != nil {
			t.Fatal(err)
		}
		poll[index] = unix.PollFd{Fd: int32(socket), Events: unix.POLLIN}
	}
	senderSocket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(senderSocket) })
	seen := [2]bool{}
	for address := uint16(2); address < markProofAddressEnd && (!seen[0] || !seen[1]); address++ {
		source := netip.MustParseAddr("10.0.0.0")
		if destination.Is6() {
			bytes := netip.MustParseAddr("fd00::").As16()
			binary.BigEndian.PutUint16(bytes[14:], address)
			source = netip.AddrFrom16(bytes)
		} else {
			bytes := source.As4()
			binary.BigEndian.PutUint16(bytes[2:], address)
			source = netip.AddrFrom4(bytes)
		}
		frame := markProofFrame(t, input, sender, source, destination)
		if err := unix.Sendto(senderSocket, frame, 0, &unix.SockaddrLinklayer{Ifindex: sender.Attrs().Index, Protocol: hairpinNetworkShort(protocol)}); err != nil {
			t.Fatal(err)
		}
		provider := markProofReceive(t, ctx, poll, source, destination)
		flow := conntrack.NewFlow(unix.IPPROTO_UDP, 0, source, destination, markProofSourcePort, markProofDestPort, markProofFlowExpiry, 0)
		current, err := offload.LookupConntrackFlow(ctx, flow.TupleOrig, 0)
		expectedMark := []uint32{markProofFirstMark, markProofSecondMark}[provider]
		if err != nil || current.Mark != expectedMark {
			t.Fatalf("packet source=%s provider=%d conntrack mark=%d expected=%d error=%v", source, provider, current.Mark, expectedMark, err)
		}
		seen[provider] = true
		t.Logf("source=%s provider=%d mark=%d", source, provider, current.Mark)
	}
	if !seen[0] || !seen[1] {
		t.Fatalf("steering did not select both configured marks: %v", seen)
	}
}

func markProofFrame(t *testing.T, input, sender netlink.Link, source, destination netip.Addr) []byte {
	t.Helper()
	ethernet := &layers.Ethernet{SrcMAC: sender.Attrs().HardwareAddr, DstMAC: input.Attrs().HardwareAddr, EthernetType: layers.EthernetTypeIPv4}
	var network gopacket.NetworkLayer
	var serialized gopacket.SerializableLayer
	if source.Is4() {
		ipv4 := &layers.IPv4{Version: 4, IHL: 5, TTL: 64, SrcIP: source.AsSlice(), DstIP: destination.AsSlice(), Protocol: layers.IPProtocolUDP}
		network, serialized = ipv4, ipv4
	} else {
		ethernet.EthernetType = layers.EthernetTypeIPv6
		ipv6 := &layers.IPv6{Version: 6, HopLimit: 64, SrcIP: source.AsSlice(), DstIP: destination.AsSlice(), NextHeader: layers.IPProtocolUDP}
		network, serialized = ipv6, ipv6
	}
	udp := &layers.UDP{SrcPort: markProofSourcePort, DstPort: markProofDestPort}
	if err := udp.SetNetworkLayerForChecksum(network); err != nil {
		t.Fatal(err)
	}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, ethernet, serialized, udp, gopacket.Payload("steering marks")); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func markProofReceive(t *testing.T, ctx context.Context, poll []unix.PollFd, source, destination netip.Addr) int {
	t.Helper()
	deadline, _ := ctx.Deadline()
	for ctx.Err() == nil {
		remaining := deadline.Sub(clock.Real{}.Now())
		timeout := min((remaining+time.Millisecond-1)/time.Millisecond, math.MaxInt32)
		if timeout <= 0 {
			break
		}
		_, err := unix.Poll(poll, int(timeout))
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for index, socket := range poll {
			if socket.Revents&unix.POLLIN == 0 {
				continue
			}
			frame := make([]byte, 2048)
			count, _, err := unix.Recvfrom(int(socket.Fd), frame, 0)
			if err != nil {
				t.Fatal(err)
			}
			packet := gopacket.NewPacket(frame[:count], layers.LayerTypeEthernet, gopacket.Default)
			if markProofForwarded(packet, source, destination) {
				return index
			}
		}
	}
	t.Fatalf("steering did not forward source=%s destination=%s before the test deadline: %v", source, destination, ctx.Err())
	return 0
}

func markProofForwarded(packet gopacket.Packet, source, destination netip.Addr) bool {
	udp, ok := packet.TransportLayer().(*layers.UDP)
	if !ok || udp.SrcPort != markProofSourcePort || udp.DstPort != markProofDestPort {
		return false
	}
	switch network := packet.NetworkLayer().(type) {
	case *layers.IPv4:
		return network.TTL == 63 && network.SrcIP.Equal(source.AsSlice()) && network.DstIP.Equal(destination.AsSlice())
	case *layers.IPv6:
		return network.HopLimit == 63 && network.SrcIP.Equal(source.AsSlice()) && network.DstIP.Equal(destination.AsSlice())
	default:
		return false
	}
}
