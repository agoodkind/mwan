//go:build linux && netns

package netif_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/netif"
)

const (
	forwardPathMark                      = 0x42
	forwardPathTable                     = 420
	forwardPathSourcePort                = 40000
	forwardPathDestinationPort           = 443
	forwardPathPolicyDSCP         uint8  = 4
	forwardPathECNValues                 = 4
	forwardPathNexthopGroupID     uint32 = 10
	forwardPathBlackholeNexthopID uint32 = 20
	// The pinned x/sys does not expose Linux RTA_NH_ID.
	forwardPathRouteNexthopAttribute = 30
)

type forwardPathFixture struct {
	selector netif.PacketSelector
	input    netlink.Link
	outputs  []netlink.Link
	peers    []netlink.Link
	gateways []net.IP
}

func forwardPathNamespace(t *testing.T, environment string) bool {
	t.Helper()
	if os.Getenv(environment) == "1" {
		return true
	}
	if os.Geteuid() != 0 {
		t.Skip("forwarding lookup requires a privileged network namespace")
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+t.Name()+"$", "-test.v")
	if deadline, ok := t.Deadline(); ok {
		remaining := deadline.Sub(clock.Real{}.Now())
		if remaining <= 0 {
			t.Fatal("network namespace child started after the test deadline")
		}
		child.Args = append(child.Args, "-test.timeout="+remaining.String())
	}
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), environment+"=1")
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated forwarding lookup: %v: %s", err, output)
	}
	t.Log(string(output))
	return false
}

func newForwardPathFixture(t *testing.T, family int, label string) forwardPathFixture {
	t.Helper()
	fixture := forwardPathFixture{}
	addresses := []string{"192.0.2.1/24", "198.51.100.1/24", "198.51.101.1/24"}
	fixture.gateways = []net.IP{net.ParseIP("198.51.100.2"), net.ParseIP("198.51.101.2")}
	source, destination := "192.0.2.2", "203.0.113.8"
	if family == unix.AF_INET6 {
		addresses = []string{"2001:db8:1::1/64", "2001:db8:2::1/64", "2001:db8:3::1/64"}
		fixture.gateways = []net.IP{net.ParseIP("2001:db8:2::2"), net.ParseIP("2001:db8:3::2")}
		source, destination = "2001:db8:1::2", "2001:db8:4::8"
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := netif.NewProcSysctlRunner(log, false)
	if err := settings.Set(t.Context(), "net.ipv4.conf.all.rp_filter", "0"); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(t.Context(), "net.ipv4.conf.default.rp_filter", "0"); err != nil {
		t.Fatal(err)
	}
	for _, familyName := range []string{"ipv4", "ipv6"} {
		if err := settings.Set(t.Context(), "net."+familyName+".conf.all.forwarding", "1"); err != nil {
			t.Fatal(err)
		}
	}
	for index, suffix := range []string{"in", "wa", "wb"} {
		name, peerName := label+suffix, label+suffix+"p"
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
		address, err := netlink.ParseAddr(addresses[index])
		if err != nil {
			t.Fatal(err)
		}
		address.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(link, address); err != nil {
			t.Fatal(err)
		}
		for _, device := range []netlink.Link{link, peer} {
			if err := netlink.LinkSetUp(device); err != nil {
				t.Fatal(err)
			}
		}
		if index == 0 {
			fixture.input = link
			continue
		}
		fixture.outputs = append(fixture.outputs, link)
		fixture.peers = append(fixture.peers, peer)
		if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: link.Attrs().Index, Family: family, State: unix.NUD_PERMANENT, IP: fixture.gateways[index-1], HardwareAddr: peer.Attrs().HardwareAddr}); err != nil {
			t.Fatal(err)
		}
	}
	fixture.selector = netif.PacketSelector{
		Source:      netip.AddrPortFrom(netip.MustParseAddr(source), forwardPathSourcePort),
		Destination: netip.AddrPortFrom(netip.MustParseAddr(destination), forwardPathDestinationPort),
		Protocol:    unix.IPPROTO_UDP, InputInterface: fixture.input.Attrs().Index, Mark: forwardPathMark,
	}
	if family == unix.AF_INET6 {
		fixture.selector.FlowLabel = 0x12345
	} else {
		fixture.selector.TrafficClass = 0x10
	}
	for index, table := range []int{unix.RT_TABLE_MAIN, forwardPathTable} {
		if err := netlink.RouteAdd(&netlink.Route{Family: family, Table: table, LinkIndex: fixture.outputs[index].Attrs().Index, Gw: fixture.gateways[index]}); err != nil {
			t.Fatal(err)
		}
	}
	rule := netlink.NewRule()
	rule.Family, rule.Priority, rule.Table = family, 100, forwardPathTable
	rule.IifName, rule.Mark, rule.IPProto = fixture.input.Attrs().Name, forwardPathMark, unix.IPPROTO_UDP
	rule.Sport = netlink.NewRulePortRange(forwardPathSourcePort, forwardPathSourcePort)
	rule.Dport = netlink.NewRulePortRange(forwardPathDestinationPort, forwardPathDestinationPort)
	sourceAddress, destinationAddress := fixture.selector.Source.Addr(), fixture.selector.Destination.Addr()
	rule.Src = &net.IPNet{IP: sourceAddress.AsSlice(), Mask: net.CIDRMask(sourceAddress.BitLen(), sourceAddress.BitLen())}
	rule.Dst = &net.IPNet{IP: destinationAddress.AsSlice(), Mask: net.CIDRMask(destinationAddress.BitLen(), destinationAddress.BitLen())}
	if family == unix.AF_INET {
		rule.Tos = uint(fixture.selector.TrafficClass)
	}
	if err := netlink.RuleAdd(rule); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertForwardPath(t *testing.T, fixture forwardPathFixture, selector netif.PacketSelector, expected int) {
	t.Helper()
	path, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), selector)
	if err != nil || !usable {
		t.Fatalf("forwarding path is unavailable: usable=%v path=%+v err=%v", usable, path, err)
	}
	if path.Route.LinkIndex != fixture.outputs[expected].Attrs().Index || path.Link.Attrs().Index != path.Route.LinkIndex || path.Neighbor.LinkIndex != path.Route.LinkIndex ||
		!path.Neighbor.IP.Equal(fixture.gateways[expected]) || !bytes.Equal(path.Neighbor.HardwareAddr, fixture.peers[expected].Attrs().HardwareAddr) {
		t.Fatalf("forwarding path differs from the kernel policy: %+v", path)
	}
}

func TestForwardPathLookupPreservesPacketSelectors(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_SELECTORS_CHILD") {
		return
	}
	for index, family := range []int{unix.AF_INET, unix.AF_INET6} {
		t.Run(fmt.Sprintf("family%d", family), func(t *testing.T) {
			fixture := newForwardPathFixture(t, family, fmt.Sprintf("f%d", index))
			assertForwardPath(t, fixture, fixture.selector, 1)
			variants := []struct {
				name   string
				change func(*netif.PacketSelector)
			}{
				{"mark", func(p *netif.PacketSelector) { p.Mark = 0 }},
				{"protocol", func(p *netif.PacketSelector) { p.Protocol = unix.IPPROTO_TCP }},
				{"source-port", func(p *netif.PacketSelector) { p.Source = netip.AddrPortFrom(p.Source.Addr(), forwardPathSourcePort+1) }},
				{"destination-port", func(p *netif.PacketSelector) {
					p.Destination = netip.AddrPortFrom(p.Destination.Addr(), forwardPathDestinationPort+1)
				}},
				{"input-interface", func(p *netif.PacketSelector) { p.InputInterface = fixture.outputs[0].Attrs().Index }},
				{"source-address", func(p *netif.PacketSelector) { p.Source = netip.AddrPortFrom(p.Source.Addr().Next(), p.Source.Port()) }},
				{"destination-address", func(p *netif.PacketSelector) {
					p.Destination = netip.AddrPortFrom(p.Destination.Addr().Next(), p.Destination.Port())
				}},
			}
			if family == unix.AF_INET {
				variants = append(variants, struct {
					name   string
					change func(*netif.PacketSelector)
				}{"traffic-class", func(p *netif.PacketSelector) { p.TrafficClass = 0 }})
			}
			for _, variant := range variants {
				t.Run(variant.name, func(t *testing.T) {
					selector := fixture.selector
					variant.change(&selector)
					assertForwardPath(t, fixture, selector, 0)
				})
			}
		})
	}
}

func TestForwardPathLookupPreservesECNIndependentRouting(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("ECN routing proof requires a privileged network namespace")
	}
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_ECN_CHILD") {
		return
	}
	for index, family := range []int{unix.AF_INET, unix.AF_INET6} {
		t.Run(fmt.Sprintf("family%d", family), func(t *testing.T) {
			fixture := newForwardPathFixture(t, family, fmt.Sprintf("e%d", index))
			fixture.selector.Protocol = unix.IPPROTO_TCP
			dscpRule := addForwardPathDSCPRule(t, family, fixture)
			for _, dscp := range []uint8{0, forwardPathPolicyDSCP} {
				for ecn := uint8(0); ecn < forwardPathECNValues; ecn++ {
					trafficClass := dscp*forwardPathECNValues | ecn
					t.Run(fmt.Sprintf("traffic-class-%02x", trafficClass), func(t *testing.T) {
						selector := fixture.selector
						selector.TrafficClass = trafficClass
						expected := 0
						if dscpRule && dscp == forwardPathPolicyDSCP {
							expected = 1
						}
						assertForwardPath(t, fixture, selector, expected)
						t.Logf("traffic_class=%#02x selected_output=%s", trafficClass, fixture.outputs[expected].Attrs().Name)
					})
				}
			}
		})
	}
}

func addForwardPathDSCPRule(t *testing.T, family int, fixture forwardPathFixture) bool {
	t.Helper()
	rule := netlink.NewRule()
	rule.Family, rule.Priority, rule.Table = family, 90, unix.RT_TABLE_MAIN
	rule.IifName, rule.Mark, rule.IPProto = fixture.input.Attrs().Name, forwardPathMark, unix.IPPROTO_TCP
	rule.Sport = netlink.NewRulePortRange(forwardPathSourcePort, forwardPathSourcePort)
	rule.Dport = netlink.NewRulePortRange(forwardPathDestinationPort, forwardPathDestinationPort)
	if err := netlink.RuleAdd(rule); err != nil {
		t.Fatalf("install the wildcard traffic-class TCP rule for family %d: %v", family, err)
	}
	rule.Priority, rule.Table, rule.Tos = 80, forwardPathTable, uint(forwardPathPolicyDSCP*forwardPathECNValues)
	if err := netlink.RuleAdd(rule); err != nil {
		if family != unix.AF_INET6 || !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.EOPNOTSUPP) {
			t.Fatalf("install the DSCP TCP rule for family %d: %v", family, err)
		}
		t.Logf("family=%d kernel rejected nonzero legacy TOS %#02x after accepting the wildcard rule: %v", family, rule.Tos, err)
		return false
	}
	t.Logf("family=%d kernel accepted nonzero legacy TOS %#02x", family, rule.Tos)
	return true
}

func TestForwardPathLookupRejectsUnavailablePaths(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_REJECT_CHILD") {
		return
	}
	fixture := newForwardPathFixture(t, unix.AF_INET, "fr")
	selector := fixture.selector
	selector.Mark = 0
	neighbor := &netlink.Neigh{LinkIndex: fixture.outputs[0].Attrs().Index, Family: unix.AF_INET, IP: fixture.gateways[0], State: unix.NUD_PERMANENT, HardwareAddr: fixture.peers[0].Attrs().HardwareAddr}
	for _, state := range []int{unix.NUD_INCOMPLETE, unix.NUD_FAILED} {
		if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: neighbor.LinkIndex, Family: neighbor.Family, IP: neighbor.IP, State: state}); err != nil {
			t.Fatal(err)
		}
		if path, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), selector); err != nil || usable {
			t.Fatalf("unresolved neighbor admitted a path: %+v %v %v", path, usable, err)
		}
	}
	if err := netlink.NeighDel(neighbor); err != nil {
		t.Fatal(err)
	}
	if _, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), selector); err != nil || usable {
		t.Fatalf("missing neighbor admitted a path: %v %v", usable, err)
	}
	if err := netlink.NeighSet(neighbor); err != nil {
		t.Fatal(err)
	}
	assertForwardPath(t, fixture, selector, 0)
	local := selector
	local.Destination = netip.AddrPortFrom(netip.MustParseAddr("192.0.2.1"), forwardPathDestinationPort)
	if _, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), local); err != nil || usable {
		t.Fatalf("local delivery admitted a forwarding path: %v %v", usable, err)
	}
	for index, routeType := range []int{unix.RTN_BLACKHOLE, unix.RTN_UNREACHABLE, unix.RTN_PROHIBIT} {
		address := netip.MustParseAddr("203.0.113.20")
		for range index {
			address = address.Next()
		}
		if err := netlink.RouteAdd(&netlink.Route{Family: unix.AF_INET, Dst: &net.IPNet{IP: address.AsSlice(), Mask: net.CIDRMask(32, 32)}, Type: routeType}); err != nil {
			t.Fatal(err)
		}
		blocked := selector
		blocked.Destination = netip.AddrPortFrom(address, forwardPathDestinationPort)
		if _, usable, _ := netif.ForwardPathLookup(t.Context(), slog.Default(), blocked); usable {
			t.Fatalf("route type %d admitted a path", routeType)
		}
	}
	encapsulated := selector
	encapsulated.Destination = netip.AddrPortFrom(netip.MustParseAddr("203.0.113.30"), forwardPathDestinationPort)
	if err := netlink.RouteAdd(&netlink.Route{
		Family: unix.AF_INET, Dst: &net.IPNet{IP: encapsulated.Destination.Addr().AsSlice(), Mask: net.CIDRMask(32, 32)},
		LinkIndex: fixture.outputs[0].Attrs().Index, Via: &netlink.Via{AddrFamily: unix.AF_INET6, Addr: net.ParseIP("fe80::2")}, Flags: unix.RTNH_F_ONLINK,
	}); err != nil {
		t.Fatal(err)
	}
	if _, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), encapsulated); err != nil || usable {
		t.Fatalf("cross-family next hop admitted a path: %v %v", usable, err)
	}
	if err := netlink.LinkSetDown(fixture.outputs[0]); err != nil {
		t.Fatal(err)
	}
	if _, usable, _ := netif.ForwardPathLookup(t.Context(), slog.Default(), selector); usable {
		t.Fatal("disabled output admitted a path")
	}
}

func addForwardPathNexthop(t *testing.T, header unix.Nhmsg, attributes *mdnetlink.AttributeEncoder) {
	t.Helper()
	executeForwardPathMessage(t, mdnetlink.Message{
		Header: mdnetlink.Header{
			Type: unix.RTM_NEWNEXTHOP, Flags: mdnetlink.Request | mdnetlink.Create | mdnetlink.Excl | mdnetlink.Acknowledge,
		},
		Data: forwardPathMessagePayload(t, header, attributes),
	})
}

func replaceForwardPathNexthopRoute(t *testing.T, selector netif.PacketSelector, id uint32) {
	t.Helper()
	destination := selector.Destination.Addr()
	family := uint8(unix.AF_INET6)
	if destination.Is4() {
		family = unix.AF_INET
	}
	header := unix.RtMsg{
		Family: family, Dst_len: uint8(destination.BitLen()), Table: unix.RT_TABLE_MAIN,
		Protocol: unix.RTPROT_BGP, Scope: unix.RT_SCOPE_UNIVERSE, Type: unix.RTN_UNICAST,
	}
	attributes := mdnetlink.NewAttributeEncoder()
	attributes.Bytes(unix.RTA_DST, destination.AsSlice())
	attributes.Uint32(forwardPathRouteNexthopAttribute, id)
	executeForwardPathMessage(t, mdnetlink.Message{
		Header: mdnetlink.Header{
			Type: unix.RTM_NEWROUTE, Flags: mdnetlink.Request | mdnetlink.Create | mdnetlink.Replace | mdnetlink.Acknowledge,
		},
		Data: forwardPathMessagePayload(t, header, attributes),
	})
}

func forwardPathMessagePayload[Header unix.Nhmsg | unix.RtMsg](t *testing.T, header Header, attributes *mdnetlink.AttributeEncoder) []byte {
	t.Helper()
	payload, err := binary.Append(nil, binary.NativeEndian, header)
	if err != nil {
		t.Fatalf("encode the forwarding fixture header: %v", err)
	}
	encodedAttributes, err := attributes.Encode()
	if err != nil {
		t.Fatalf("encode the forwarding fixture attributes: %v", err)
	}
	return append(payload, encodedAttributes...)
}

func executeForwardPathMessage(t *testing.T, message mdnetlink.Message) {
	t.Helper()
	socket, err := mdnetlink.Dial(unix.NETLINK_ROUTE, nil)
	if err != nil {
		t.Fatalf("open the forwarding fixture socket: %v", err)
	}
	defer func() {
		if err := socket.Close(); err != nil {
			t.Errorf("close the forwarding fixture socket: %v", err)
		}
	}()
	if deadline, ok := t.Deadline(); ok {
		if err := socket.SetDeadline(deadline); err != nil {
			t.Fatalf("set the forwarding fixture deadline: %v", err)
		}
	}
	if _, err := socket.Execute(message); err != nil {
		t.Fatalf("configure the forwarding fixture with message type %d: %v", message.Header.Type, err)
	}
}

func TestForwardPathLookupRequiresActualMultipathObservation(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_MULTIPATH_CHILD") {
		return
	}
	fixture := newForwardPathFixture(t, unix.AF_INET, "fm")
	selector := fixture.selector
	selector.Mark = 0
	destination := &net.IPNet{IP: selector.Destination.Addr().AsSlice(), Mask: net.CIDRMask(32, 32)}
	if err := netlink.RouteAdd(&netlink.Route{Family: unix.AF_INET, Dst: destination, MultiPath: []*netlink.NexthopInfo{
		{LinkIndex: fixture.outputs[0].Attrs().Index, Gw: fixture.gateways[0]}, {LinkIndex: fixture.outputs[1].Attrs().Index, Gw: fixture.gateways[1]},
	}}); err != nil {
		t.Fatal(err)
	}
	path, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), selector)
	var requirement *netif.EgressObservationRequiredError
	if usable || !errors.As(err, &requirement) || requirement.Reason != netif.EgressObservationMultipath || path.Link == nil || len(path.Neighbor.HardwareAddr) != 6 {
		t.Fatalf("multipath admission did not require actual egress: path=%+v usable=%v err=%v", path, usable, err)
	}
	for index, output := range fixture.outputs {
		attributes := mdnetlink.NewAttributeEncoder()
		attributes.Uint32(unix.NHA_ID, uint32(index+1))
		attributes.Uint32(unix.NHA_OIF, uint32(output.Attrs().Index))
		attributes.Bytes(unix.NHA_GATEWAY, fixture.gateways[index].To4())
		addForwardPathNexthop(t, unix.Nhmsg{Family: unix.AF_INET, Protocol: unix.RTPROT_BGP}, attributes)
	}
	settings := netif.NewProcSysctlRunner(slog.Default(), false)
	if err := settings.Set(t.Context(), "net.ipv4.nexthop_compat_mode", "0"); err != nil {
		t.Fatal(err)
	}
	group, err := binary.Append(nil, binary.NativeEndian, []unix.NexthopGrp{{Id: 1}, {Id: 2}})
	if err != nil {
		t.Fatalf("encode the nexthop group members: %v", err)
	}
	attributes := mdnetlink.NewAttributeEncoder()
	attributes.Uint32(unix.NHA_ID, forwardPathNexthopGroupID)
	attributes.Bytes(unix.NHA_GROUP, group)
	addForwardPathNexthop(t, unix.Nhmsg{}, attributes)
	replaceForwardPathNexthopRoute(t, selector, forwardPathNexthopGroupID)
	path, usable, err = netif.ForwardPathLookup(t.Context(), slog.Default(), selector)
	if usable || !errors.As(err, &requirement) || requirement.Reason != netif.EgressObservationNexthopGroup || requirement.NexthopID != forwardPathNexthopGroupID || path.Link == nil {
		t.Fatalf("hidden nexthop group admitted a synthetic path: %+v %v %v", path, usable, err)
	}
	replaceForwardPathNexthopRoute(t, selector, 1)
	assertForwardPath(t, fixture, selector, 0)
}

func TestForwardPathLookupPreservesIPv6FlowLabel(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_FLOWLABEL_CHILD") {
		return
	}
	fixture := newForwardPathFixture(t, unix.AF_INET6, "fl")
	selector := fixture.selector
	selector.Mark = 0
	settings := netif.NewProcSysctlRunner(slog.Default(), false)
	if err := settings.Set(t.Context(), "net.ipv6.fib_multipath_hash_policy", "0"); err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		Family: unix.AF_INET6, Dst: &net.IPNet{IP: selector.Destination.Addr().AsSlice(), Mask: net.CIDRMask(128, 128)},
		MultiPath: []*netlink.NexthopInfo{{LinkIndex: fixture.outputs[0].Attrs().Index, Gw: fixture.gateways[0]}, {LinkIndex: fixture.outputs[1].Attrs().Index, Gw: fixture.gateways[1]}},
	}); err != nil {
		t.Fatal(err)
	}
	outputs := make(map[int]bool)
	for label := uint32(0x10000); label < 0x10040; label++ {
		selector.FlowLabel = label
		path, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), selector)
		var requirement *netif.EgressObservationRequiredError
		if usable || !errors.As(err, &requirement) || requirement.Reason != netif.EgressObservationMultipath || path.Link == nil {
			t.Fatalf("flow-label query did not require observation: %+v %v %v", path, usable, err)
		}
		outputs[path.Link.Attrs().Index] = true
		if len(outputs) == 2 {
			break
		}
	}
	if len(outputs) != 2 {
		t.Fatalf("flow-label variation selected %d outputs, want both", len(outputs))
	}
}
