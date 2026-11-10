//go:build linux && netns

package steering

import (
	"context"
	"encoding/binary"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/ifmgr"
)

func TestPinnedFlowRespectsFamilyEligibility(t *testing.T) {
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
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	lan, client := hairpinVeth(t, "lan", "client")
	badWAN, badPeer := hairpinVeth(t, "badwan", "badpeer")
	goodWAN, goodPeer := hairpinVeth(t, "goodwan", "goodpeer")
	if err := netlink.AddrAdd(lan, &netlink.Addr{IPNet: &net.IPNet{IP: net.IPv4(10, 0, 0, 1), Mask: net.CIDRMask(24, 32)}}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		link  netlink.Link
		table int
	}{{badWAN, unix.RT_TABLE_MAIN}, {badWAN, 100}, {goodWAN, 200}} {
		if err := netlink.RouteAdd(&netlink.Route{
			LinkIndex: item.link.Attrs().Index, Table: item.table,
			Dst: &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		}); err != nil {
			t.Fatal(err)
		}
		if err := netlink.NeighSet(&netlink.Neigh{
			LinkIndex: item.link.Attrs().Index,
			IP:        net.IPv4(198, 51, 100, 8), HardwareAddr: peerFor(item.link, badWAN, badPeer, goodPeer).Attrs().HardwareAddr,
			State: netlink.NUD_PERMANENT,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.NeighSet(&netlink.Neigh{
		LinkIndex: lan.Attrs().Index,
		IP:        net.IPv4(10, 0, 0, 3), HardwareAddr: client.Attrs().HardwareAddr,
		State: netlink.NUD_PERMANENT,
	}); err != nil {
		t.Fatal(err)
	}
	routeRule := netlink.NewRule()
	routeRule.Family, routeRule.Priority, routeRule.Mark, routeRule.Table = unix.AF_INET, 100, 2, 200
	if err := netlink.RuleAdd(routeRule); err != nil {
		t.Fatal(err)
	}
	firstProviderRule := netlink.NewRule()
	firstProviderRule.Family, firstProviderRule.Priority, firstProviderRule.Mark, firstProviderRule.Table = unix.AF_INET, 101, 1, 100
	if err := netlink.RuleAdd(firstProviderRule); err != nil {
		t.Fatal(err)
	}
	installLatePin(t, "lan", 1)
	installLegacySteeringChain(t)
	input := ruleInput{
		InternalIface: "lan", InternalNetV4: netip.MustParsePrefix("10.0.0.0/24"),
		InternalPrefix: netip.MustParsePrefix("fd00::/64"),
		OpnsenseEdgeV6: netip.MustParseAddr("fd00::1"), Mode: hashModeRandom,
		AssignV4: balancer{Mark: 2},
		Members: []Member{
			{WANRef: ifmgr.WANRef{Name: "bad", Iface: "badwan"}, Mark: 1},
			{WANRef: ifmgr.WANRef{Name: "good", Iface: "goodwan"}, Mark: 2},
		},
		EligibleV4: map[uint32]bool{2: true},
	}
	if err := newNFTApplier().Apply(context.Background(), slog.Default(), buildRules(input)); err != nil {
		t.Fatal(err)
	}
	assertSteeringPriority(t, steerChainPriority)
	goodSocket := packetSocket(t, goodPeer.Attrs().Index)
	sendIPv4Packet(t, lan, client, net.IPv4(198, 51, 100, 8), 30001)
	if !receiveIPv4Packet(goodSocket, net.IPv4(198, 51, 100, 8), 30001) {
		t.Fatal("pinned packet did not exit through the eligible provider")
	}
	input.EligibleV4 = map[uint32]bool{1: true, 2: true}
	if err := newNFTApplier().Apply(context.Background(), slog.Default(), buildRules(input)); err != nil {
		t.Fatal(err)
	}
	fallbackRule := netlink.NewRule()
	fallbackRule.Family, fallbackRule.Priority, fallbackRule.IifName, fallbackRule.Table = unix.AF_INET, 50, "lan", 200
	if err := netlink.RuleAdd(fallbackRule); err != nil {
		t.Fatal(err)
	}
	sendIPv4Packet(t, lan, client, net.IPv4(198, 51, 100, 8), 30004)
	if !receiveIPv4Packet(goodSocket, net.IPv4(198, 51, 100, 8), 30004) {
		t.Fatal("priority-50 fallback rejected a mark for another eligible provider")
	}
	if err := netlink.RuleDel(fallbackRule); err != nil {
		t.Fatal(err)
	}
	if err := netlink.RuleDel(firstProviderRule); err != nil {
		t.Fatal(err)
	}
	input.AssignV4 = balancer{}
	input.EligibleV4 = nil
	if err := newNFTApplier().Apply(context.Background(), slog.Default(), buildRules(input)); err != nil {
		t.Fatal(err)
	}
	badSocket := packetSocket(t, badPeer.Attrs().Index)
	sendIPv4Packet(t, lan, client, net.IPv4(198, 51, 100, 8), 30002)
	if receiveIPv4Packet(badSocket, net.IPv4(198, 51, 100, 8), 30002) {
		t.Fatal("packet exited through the ineligible main-table provider")
	}
	if receiveIPv4Packet(goodSocket, net.IPv4(198, 51, 100, 8), 30002) {
		t.Fatal("packet exited through a provider when neither was eligible")
	}
	clientSocket := packetSocket(t, client.Attrs().Index)
	sendIPv4Packet(t, lan, client, net.IPv4(10, 0, 0, 3), 30003)
	if !receiveIPv4Packet(clientSocket, net.IPv4(10, 0, 0, 3), 30003) {
		t.Fatal("internal delivery failed with no eligible provider")
	}
}

func peerFor(link, badWAN, badPeer, goodPeer netlink.Link) netlink.Link {
	if link.Attrs().Index == badWAN.Attrs().Index {
		return badPeer
	}
	return goodPeer
}

func installLegacySteeringChain(t *testing.T) {
	t.Helper()
	conn, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	table := conn.AddTable(&nftables.Table{Name: steerTableName, Family: nftables.TableFamilyINet})
	priority, policy := nftables.ChainPriority(-149), nftables.ChainPolicyAccept
	conn.AddChain(&nftables.Chain{
		Name: steerChainName, Table: table,
		Hooknum: nftables.ChainHookPrerouting, Priority: &priority,
		Type: nftables.ChainTypeFilter, Policy: &policy,
	})
	if err := conn.Flush(); err != nil {
		t.Fatal(err)
	}
	assertSteeringPriority(t, priority)
}

func assertSteeringPriority(t *testing.T, want nftables.ChainPriority) {
	t.Helper()
	conn, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	chain, err := conn.ListChain(&nftables.Table{Name: steerTableName, Family: nftables.TableFamilyINet}, steerChainName)
	if err != nil {
		t.Fatal(err)
	}
	if chain.Priority == nil || *chain.Priority != want {
		t.Fatalf("steering priority = %v, want %d", chain.Priority, want)
	}
}

func installLatePin(t *testing.T, internal string, mark uint32) {
	t.Helper()
	conn, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	table := conn.AddTable(&nftables.Table{Name: "test_pin", Family: nftables.TableFamilyINet})
	priority, policy := nftables.ChainPriority(-100), nftables.ChainPolicyAccept
	chain := conn.AddChain(&nftables.Chain{
		Name: "prerouting", Table: table,
		Hooknum: nftables.ChainHookPrerouting, Priority: &priority,
		Type: nftables.ChainTypeFilter, Policy: &policy,
	})
	conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{
		&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: ifname(internal)},
		&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(mark)},
		&expr.Meta{Key: expr.MetaKeyMARK, SourceRegister: true, Register: 1},
	}})
	if err := conn.Flush(); err != nil {
		t.Fatal(err)
	}
}

func packetSocket(t *testing.T, index int) int {
	t.Helper()
	socket, err := unix.Socket(unix.AF_PACKET, unix.SOCK_RAW, int(hairpinNetworkShort(unix.ETH_P_IP)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(socket) })
	if err := unix.Bind(socket, &unix.SockaddrLinklayer{
		Ifindex:  index,
		Protocol: hairpinNetworkShort(unix.ETH_P_IP),
	}); err != nil {
		t.Fatal(err)
	}
	if err := unix.SetsockoptTimeval(socket, unix.SOL_SOCKET, unix.SO_RCVTIMEO,
		&unix.Timeval{Sec: 1}); err != nil {
		t.Fatal(err)
	}
	return socket
}

func sendIPv4Packet(t *testing.T, lan, client netlink.Link, destination net.IP, sourcePort uint16) {
	t.Helper()
	socket := packetSocket(t, client.Attrs().Index)
	frame := make([]byte, 14+20+8)
	copy(frame[:6], lan.Attrs().HardwareAddr)
	copy(frame[6:12], client.Attrs().HardwareAddr)
	binary.BigEndian.PutUint16(frame[12:14], unix.ETH_P_IP)
	frame[14], frame[15], frame[22], frame[23] = 0x45, 0, 64, 17
	binary.BigEndian.PutUint16(frame[16:18], 28)
	copy(frame[26:30], net.IPv4(10, 0, 0, 2).To4())
	copy(frame[30:34], destination.To4())
	binary.BigEndian.PutUint16(frame[34:36], sourcePort)
	binary.BigEndian.PutUint16(frame[36:38], 443)
	binary.BigEndian.PutUint16(frame[38:40], 8)
	binary.BigEndian.PutUint16(frame[24:26], ipv4Checksum(frame[14:34]))
	if err := unix.Sendto(socket, frame, 0, &unix.SockaddrLinklayer{
		Ifindex: client.Attrs().Index, Protocol: hairpinNetworkShort(unix.ETH_P_IP),
	}); err != nil {
		t.Fatal(err)
	}
}

func receiveIPv4Packet(socket int, destination net.IP, sourcePort uint16) bool {
	frame := make([]byte, 2048)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		count, from, err := unix.Recvfrom(socket, frame, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false
		}
		if link, ok := from.(*unix.SockaddrLinklayer); ok && link.Pkttype == unix.PACKET_OUTGOING {
			continue
		}
		if count >= 42 && frame[22] == 63 && net.IP(frame[30:34]).Equal(destination) &&
			binary.BigEndian.Uint16(frame[34:36]) == sourcePort {
			return true
		}
	}
	return false
}

func ipv4Checksum(header []byte) uint16 {
	var sum uint32
	for i := 0; i < len(header); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(header[i : i+2]))
	}
	for sum > 0xffff {
		sum = sum&0xffff + sum>>16
	}
	return ^uint16(sum)
}
