//go:build linux && netns

package steering

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
)

func TestHairpinPacketKeepsInternalRoute(t *testing.T) {
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
	lan, client := hairpinVeth(t, "lan", "client")
	wan, _ := hairpinVeth(t, "wan", "internet")
	internal := netip.MustParseAddr("fd01:203:405:1::1234")
	secondInternal := netip.MustParseAddr("fd01:203:405:2::5678")
	secondExternal := netip.MustParseAddr("2001:db8:1:d551::5678")
	pair := bpf.PrefixPair{
		ID: 7, Internal: netip.MustParsePrefix("fd01:203:405::/48"),
		External: netip.MustParsePrefix("2001:db8:1::/48"),
	}
	_, internalNetwork, err := net.ParseCIDR(pair.Internal.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: lan.Attrs().Index, Dst: internalNetwork}); err != nil {
		t.Fatalf("internal route: %v", err)
	}
	if err := netlink.NeighSet(&netlink.Neigh{
		LinkIndex: lan.Attrs().Index, IP: net.IP(secondInternal.AsSlice()),
		HardwareAddr: client.Attrs().HardwareAddr, State: netlink.NUD_PERMANENT,
	}); err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: wan.Attrs().Index, Table: 200,
		Dst: &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, 128)},
	}); err != nil {
		t.Fatalf("provider route: %v", err)
	}
	rule := netlink.NewRule()
	rule.Family = unix.AF_INET6
	rule.Priority = 100
	rule.Mark = 2
	rule.Table = 200
	if err := netlink.RuleAdd(rule); err != nil {
		t.Fatalf("provider rule: %v", err)
	}
	translator, err := bpf.New()
	if err != nil {
		t.Fatal(err)
	}
	defer translator.Close()
	states, err := translator.Reconcile([]bpf.InterfacePolicy{{
		IfIndex: lan.Attrs().Index, Internal: true, Pairs: []bpf.PrefixPair{pair},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range states {
		if !state.Ready {
			t.Fatalf("NPT attachment is not ready: %+v", state)
		}
	}
	rules := buildRules(ruleInput{
		InternalIface:  "lan",
		InternalNetV4:  netip.MustParsePrefix("10.250.250.0/29"),
		InternalPrefix: pair.Internal,
		OpnsenseEdgeV6: netip.MustParseAddr("fd01:203:405::1"),
		Mode:           hashModeRandom,
		AssignV6:       balancer{Mark: 2},
	})
	if err := newNFTApplier().Apply(context.Background(), slog.Default(), rules); err != nil {
		t.Fatal(err)
	}
	socket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(hairpinNetworkShort(unix.ETH_P_IPV6)))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(socket)
	if err := unix.Bind(socket, &unix.SockaddrLinklayer{
		Ifindex: client.Attrs().Index, Protocol: hairpinNetworkShort(unix.ETH_P_IPV6),
	}); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetsockoptTimeval(socket, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2}); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 14+40+8+8)
	copy(frame[:6], lan.Attrs().HardwareAddr)
	copy(frame[6:12], client.Attrs().HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], unix.ETH_P_IPV6)
	frame[14] = 0x60
	binary.BigEndian.PutUint16(frame[18:20], 16)
	frame[20], frame[21] = 17, 64
	copy(frame[22:38], internal.AsSlice())
	copy(frame[38:54], secondExternal.AsSlice())
	binary.BigEndian.PutUint16(frame[54:56], 12345)
	binary.BigEndian.PutUint16(frame[56:58], 23456)
	binary.BigEndian.PutUint16(frame[58:60], 16)
	copy(frame[62:], []byte("hairpin!"))
	binary.BigEndian.PutUint16(frame[60:62], hairpinChecksum(frame))
	if err := unix.Sendto(socket, frame, 0, &unix.SockaddrLinklayer{
		Ifindex: client.Attrs().Index, Protocol: hairpinNetworkShort(unix.ETH_P_IPV6),
	}); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 2048)
	for {
		count, _, err := unix.Recvfrom(socket, data, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			t.Fatalf("internal hairpin packet was not forwarded to the client: %v", err)
		}
		if count != len(frame) || data[21] != 63 || !bytes.Equal(data[62:count], []byte("hairpin!")) {
			continue
		}
		destination := netip.AddrFrom16([16]byte(data[38:54]))
		if destination != secondInternal {
			t.Fatalf("hairpin destination = %s, want %s", destination, secondInternal)
		}
		return
	}
}

func hairpinVeth(t *testing.T, name, peerName string) (netlink.Link, netlink.Link) {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: peerName,
	}); err != nil {
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

func hairpinNetworkShort(value uint16) uint16 { return value>>8 | value<<8 }

func hairpinChecksum(frame []byte) uint16 {
	var sum uint32
	for i := 22; i < 54; i += 2 {
		sum += uint32(binary.BigEndian.Uint16(frame[i : i+2]))
	}
	sum += uint32(len(frame)-54) + uint32(frame[20])
	for i := 54; i < len(frame); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(frame[i : i+2]))
	}
	for sum > 0xffff {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}
