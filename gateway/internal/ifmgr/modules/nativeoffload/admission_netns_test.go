//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"testing"
	"time"

	"github.com/agoodkind/netlink"
	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	mdnetlink "github.com/mdlayher/netlink"
	"github.com/ti-mo/conntrack"
	"github.com/ti-mo/netfilter"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/nptpolicy"
	"goodkind.io/mwan/internal/offload"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	admissionTimeout                                 = 30 * time.Second
	admissionFlowTimeout                             = 120
	admissionMark              uint32                = 73
	admissionOtherMark         uint32                = 74
	admissionUnknownMark       uint32                = 99
	admissionRouteTable                              = 420
	admissionRoutePriority                           = 100
	admissionTrafficClass                            = 0x10
	admissionLANID                                   = "admission-lan"
	admissionWANID                                   = "admission-wan"
	admissionOtherID                                 = "admission-other"
	admissionWANName                                 = "native-wan"
	admissionWANPeerName                             = "native-wanpeer"
	admissionOtherName                               = "native-alt"
	admissionOtherPeerName                           = "native-altpeer"
	admissionClientV4                                = "192.0.2.10"
	admissionExternalClientV4                        = "198.51.100.10"
	admissionRemoteV4                                = "203.0.113.20"
	admissionClientV6                                = "2001:db8:1::10"
	admissionInternalClientV6                        = "fd01:203:405::10"
	admissionRemoteV6                                = "2001:db8:9::20"
	admissionInternalPrefixV6                        = "fd01:203:405::/48"
	admissionExternalPrefixV6                        = "2001:db8:100::/48"
	admissionExternalClientV6                        = "2001:db8:100:d450::10"
	admissionPrivateBPFFS                            = "/sys/fs/bpf"
	admissionCTNew             netfilter.MessageType = 0
	admissionCTTupleOriginal   uint16                = 1
	admissionCTTupleReply      uint16                = 2
	admissionCTStatus          uint16                = 3
	admissionCTProtocolInfo    uint16                = 4
	admissionCTNATSource       uint16                = 6
	admissionCTTimeout         uint16                = 7
	admissionCTMark            uint16                = 8
	admissionCTTupleIP         uint16                = 1
	admissionCTTupleProtocol   uint16                = 2
	admissionCTIPv4Source      uint16                = 1
	admissionCTIPv4Destination uint16                = 2
	admissionCTProtocolNumber  uint16                = 1
	admissionCTSourcePort      uint16                = 2
	admissionCTDestinationPort uint16                = 3
	admissionCTTCPInfo         uint16                = 1
	admissionCTTCPState        uint16                = 1
	admissionCTNATMinimumIP    uint16                = 1
)

type admissionFixture struct {
	ctx       context.Context
	module    *Module
	store     *wanstate.Store
	tracking  *conntrack.Conn
	input     *net.Interface
	sender    *net.Interface
	output    *net.Interface
	peer      *net.Interface
	other     *net.Interface
	otherPeer *net.Interface
	neighbors map[int]netlink.Neigh
	rules     firewall.Ruleset
}

func TestNativeAdmissionIPv4(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	flow := fixture.flow(t, admissionClientV4, admissionRemoteV4, "")
	observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	fixture.requireCandidate(t, admissionObservation(t, fixture.peer, fixture.output, flow.TupleReply), flow, flow.TupleReply, 1, fixture.input, fixture.sender)
}

func TestNativeAdmissionStaticSNAT44(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNAPT44)
	flow := fixture.flow(t, admissionClientV4, admissionRemoteV4, admissionExternalClientV4)
	translated := flow.TupleOrig
	translated.IP.SourceAddress = netip.MustParseAddr(admissionExternalClientV4)
	fixture.requireCandidate(t, admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig), flow, translated, 0, fixture.output, fixture.peer)
	reply := flow.TupleReply
	reply.IP.DestinationAddress = netip.MustParseAddr(admissionClientV4)
	fixture.requireCandidate(t, admissionObservation(t, fixture.peer, fixture.output, flow.TupleReply), flow, reply, 1, fixture.input, fixture.sender)
}

func TestNativeAdmissionIPv6(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	flow := fixture.flow(t, admissionClientV6, admissionRemoteV6, "")
	fixture.requireCandidate(t, admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig), flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	fixture.requireCandidate(t, admissionObservation(t, fixture.peer, fixture.output, flow.TupleReply), flow, flow.TupleReply, 1, fixture.input, fixture.sender)
}

func TestNativeAdmissionNPTv6(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	admissionPrivateBPFMount(t)
	translator, err := bpf.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := translator.Close(); err != nil {
			t.Error(err)
		}
	})
	pair := nptpolicy.PrefixPair{
		ID: 1, Internal: netip.MustParsePrefix(admissionInternalPrefixV6), External: netip.MustParsePrefix(admissionExternalPrefixV6),
	}
	states, err := translator.Reconcile([]bpf.InterfacePolicy{
		{IfIndex: fixture.input.Index, Internal: true, Pairs: []nptpolicy.PrefixPair{pair}},
		{IfIndex: fixture.output.Index, Pairs: []nptpolicy.PrefixPair{pair}},
	})
	if err != nil || len(states) != 4 {
		t.Fatalf("NPT policy did not install both hooks on both interfaces: states=%+v error=%v", states, err)
	}
	for _, state := range states {
		if !state.Ready {
			t.Fatalf("NPT kernel readback did not confirm the applied policy: %+v", state)
		}
	}
	translations := fixture.store.Snapshot().Translation
	member := translations[admissionWANID]
	member.V6 = wanstate.FamilyTranslation{
		Mode: string(config.TranslationNPTv6), Ready: true, InternalPrefix: pair.Internal, ExternalPrefix: pair.External,
	}
	translations[admissionWANID] = member
	fixture.store.SetNPTTranslation(translations, map[string]nptpolicy.PrefixPair{admissionWANID: pair})
	flow := fixture.flow(t, admissionInternalClientV6, admissionRemoteV6, "")
	translated := flow.TupleOrig
	translated.IP.SourceAddress = netip.MustParseAddr(admissionExternalClientV6)
	fixture.requireCandidate(t, admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig), flow, translated, 0, fixture.output, fixture.peer)
	wireReply := flow.TupleReply
	wireReply.IP.DestinationAddress = netip.MustParseAddr(admissionExternalClientV6)
	fixture.requireCandidate(t, admissionObservation(t, fixture.peer, fixture.output, wireReply), flow, flow.TupleReply, 1, fixture.input, fixture.sender)
	previous := fixture.store.Snapshot().NPTPolicyGeneration
	if _, err := translator.Reconcile(nil); err != nil {
		t.Fatal(err)
	}
	for _, link := range []*net.Interface{fixture.input, fixture.output} {
		current, err := netlink.LinkByIndex(link.Index)
		if err != nil {
			t.Fatal(err)
		}
		for _, parent := range []uint32{netlink.HANDLE_MIN_INGRESS, netlink.HANDLE_MIN_EGRESS} {
			filters, err := netlink.FilterList(current, parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, filter := range filters {
				if attached, ok := filter.(*netlink.BpfFilter); ok && (attached.Name == "mwan-npt-ingress" || attached.Name == "mwan-npt-egress") {
					t.Fatal("NPT policy removal retained a translation hook")
				}
			}
		}
	}
	member.V6.Ready = false
	translations[admissionWANID] = member
	fixture.store.SetNPTTranslation(translations, nil)
	if fixture.store.Snapshot().NPTPolicyGeneration <= previous {
		t.Fatal("NPT removal did not replace the applied policy generation")
	}
	fixture.requireRejected(t, admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig), EligibilityPolicyUnavailable)
}

func TestNativeAdmissionRejectsStaleEvidence(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	flow := fixture.flow(t, admissionClientV4, admissionRemoteV4, "")
	observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	stale := flow
	stale.ID++
	if _, err := offload.FreshConntrackFlow(fixture.ctx, stale); err == nil {
		t.Fatal("native admission accepted a different kernel connection identity")
	}
	stale.ID = 0
	if _, err := offload.FreshConntrackFlow(fixture.ctx, stale); err == nil {
		t.Fatal("native admission accepted a missing kernel connection identity")
	}
	neighbor := fixture.neighbors[unix.AF_INET]
	if err := netlink.NeighDel(&neighbor); err != nil {
		t.Fatal(err)
	}
	fixture.requireRejected(t, observation, EligibilityUnsupported)
	if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: neighbor.LinkIndex, Family: neighbor.Family, IP: neighbor.IP, State: unix.NUD_FAILED}); err != nil {
		t.Fatal(err)
	}
	fixture.requireRejected(t, observation, EligibilityUnsupported)
	neighbor.HardwareAddr = net.HardwareAddr{2, 0, 0, 0, 0, 99}
	if err := netlink.NeighSet(&neighbor); err != nil {
		t.Fatal(err)
	}
	spec, _, _, _, decision, err := fixture.module.candidate(fixture.ctx, observation)
	if err != nil || decision != EligibilityReady || spec.Directions[0].OutputDestinationMAC != [6]byte(neighbor.HardwareAddr) {
		t.Fatalf("candidate did not replace the changed neighbor MAC from the kernel: %+v decision=%d error=%v", spec, decision, err)
	}
	neighbor.HardwareAddr = fixture.peer.HardwareAddr
	if err := netlink.NeighSet(&neighbor); err != nil {
		t.Fatal(err)
	}
	if err := fixture.tracking.Update(conntrack.Flow{TupleOrig: flow.TupleOrig, Mark: admissionUnknownMark}); err != nil {
		t.Fatal(err)
	}
	fixture.requireRejected(t, observation, EligibilityPolicyUnavailable)
	if err := fixture.tracking.Update(conntrack.Flow{TupleOrig: flow.TupleOrig, Mark: admissionMark}); err != nil {
		t.Fatal(err)
	}
	baseline, err := firewall.CompileBaseline(firewall.BaselineConfig{
		ManagementPolicy: firewall.ManagementOptional, InternalInterface: fixture.input.Name,
		ProviderInterfaces: []string{fixture.output.Name, fixture.other.Name},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := firewall.Apply(fixture.ctx, baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := firewall.Inspect(fixture.ctx, baseline); err != nil {
		t.Fatal(err)
	}
	if _, err := firewall.Inspect(fixture.ctx, fixture.rules); err == nil {
		t.Fatal("restricted firewall incorrectly matched the forwarding policy")
	}
	fixture.store.SetFirewallProtection(admissionWANID, "ipv4", "not-ready")
	fixture.requireRejected(t, observation, EligibilityPolicyUnavailable)
	fixture.publishFirewall(t)
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	if err := fixture.tracking.Delete(flow); err != nil {
		t.Fatal(err)
	}
	fixture.requireRejected(t, observation, EligibilityStale)
	if _, err := offload.FreshConntrackFlow(fixture.ctx, flow); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("deleted kernel identity remained eligible: %v", err)
	}
}

func newAdmissionFixture(t *testing.T, mode config.TranslationMode) admissionFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), admissionTimeout)
	t.Cleanup(cancel)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	settings := netif.NewProcSysctlRunner(log, false)
	for key, value := range map[string]string{
		"net.ipv4.conf.all.rp_filter": "0", "net.ipv4.conf.default.rp_filter": "0",
		"net.ipv4.conf.all.forwarding": "1", "net.ipv6.conf.all.forwarding": "1",
	} {
		if err := settings.Set(ctx, key, value); err != nil {
			t.Fatalf("set admission sysctl %s=%s: %v", key, value, err)
		}
	}
	input, sender := nativeCaptureLinks(t)
	output, peer := admissionVeth(t, admissionWANName, admissionWANPeerName, 3)
	other, otherPeer := admissionVeth(t, admissionOtherName, admissionOtherPeerName, 5)
	for _, entry := range []struct {
		link    *net.Interface
		address string
	}{
		{input, "192.0.2.1/24"}, {input, "2001:db8:1::1/64"}, {input, "fd01:203:405::1/64"},
		{output, "198.51.100.1/24"}, {output, "2001:db8:2::1/64"},
		{other, "198.51.101.1/24"}, {other, "2001:db8:3::1/64"},
	} {
		admissionAddress(t, entry.link, entry.address)
	}
	connections := []interfaceintent.Connection{
		{ID: admissionLANID, Name: input.Name, Owner: interfaceintent.OwnerMWAN, Roles: interfaceintent.RoleInternal},
		{ID: admissionWANID, Name: output.Name, Owner: interfaceintent.OwnerMWAN, Roles: interfaceintent.RoleProvider},
		{ID: admissionOtherID, Name: other.Name, Owner: interfaceintent.OwnerMWAN, Roles: interfaceintent.RoleProvider},
	}
	policy := &config.IPv4Translation{Mode: mode}
	provider := firewall.Provider{Interface: output.Name, Mark: admissionMark, ForcedDSCP: admissionTrafficClass / 4}
	if mode == config.TranslationNAPT44 {
		policy.StaticMappings = []config.StaticMapping{{Internal: netip.MustParseAddr(admissionClientV4), External: netip.MustParseAddr(admissionExternalClientV4)}}
		provider.MasqueradeIPv4 = true
		provider.StaticMappings = []firewall.Mapping{{Internal: policy.StaticMappings[0].Internal, External: policy.StaticMappings[0].External}}
	}
	firewallConfig := firewall.Config{
		Enabled: true, ManagementPolicy: firewall.ManagementOptional, InternalInterface: input.Name,
		InternalNetworkIPv4: netip.MustParsePrefix("192.0.2.0/24"), KnownInterfaces: []string{input.Name, output.Name, other.Name},
		Paths:     []firewall.ForwardingPath{{InternalInterface: input.Name, ExternalInterface: output.Name, IPv4: true, IPv6: true}, {InternalInterface: input.Name, ExternalInterface: other.Name, IPv4: true, IPv6: true}},
		Providers: []firewall.Provider{provider, {Interface: other.Name, Mark: admissionOtherMark}},
	}
	store := wanstate.New()
	store.SetConnections(connections)
	created, err := New(Config{
		Enabled: true, Firewall: firewallConfig,
		WANs: []WAN{
			{WANRef: ifmgr.WANRef{ID: admissionOtherID, Name: admissionOtherID, Iface: other.Name}, Mark: admissionOtherMark, TranslationV4: &config.IPv4Translation{Mode: config.TranslationNative}},
			{WANRef: ifmgr.WANRef{ID: admissionWANID, Name: admissionWANID, Iface: output.Name}, Mark: admissionMark, TranslationV4: policy},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	module := created.(*Module)
	module.InitBase(&ifmgr.Env{Log: log, LiveState: store, Connections: connections})
	module.policyGeneration.Store(1)
	module.links = make(map[int]boundLink)
	for index, link := range []*net.Interface{input, output, other} {
		current, err := netlink.LinkByIndex(link.Index)
		if err != nil {
			t.Fatalf("inspect admission link %s index %d: %v", link.Name, link.Index, err)
		}
		module.links[link.Index] = boundLink{
			connection: connections[index], kernel: *link, identity: netif.LinkOwnershipIdentity(current),
			data: offload.NativeDataID(index), origin: offload.NativeOriginID(index), hasOrigin: true,
		}
	}
	tracking, err := conntrack.Dial(nil)
	if err != nil {
		t.Fatalf("open admission fixture conntrack socket: %v", err)
	}
	t.Cleanup(func() {
		if err := tracking.Close(); err != nil {
			t.Error(err)
		}
	})
	rules, err := firewall.Compile(firewallConfig)
	if err != nil {
		t.Fatalf("compile admission firewall: %v", err)
	}
	fixture := admissionFixture{ctx: ctx, module: module, store: store, tracking: tracking, input: input, sender: sender, output: output, peer: peer, other: other, otherPeer: otherPeer, neighbors: make(map[int]netlink.Neigh), rules: rules}
	fixture.preparePaths(t)
	fixture.publishFirewall(t)
	for _, connection := range connections[1:] {
		fixture.publishObservation(t, connection)
	}
	translations := make(map[string]wanstate.MemberTranslation)
	for _, wan := range module.cfg.WANs {
		translation := npt.IPv4TranslationReadiness(ctx, npt.WAN{WANRef: wan.WANRef, TranslationV4: wan.TranslationV4}, firewallConfig.InternalNetworkIPv4)
		if !translation.Ready {
			t.Fatalf("applied IPv4 translation did not pass kernel inspection: %+v", translation)
		}
		translations[wan.Key()] = wanstate.MemberTranslation{V4: translation, V6: wanstate.FamilyTranslation{Mode: string(config.TranslationNative), Ready: true}}
	}
	store.SetTranslation(translations)
	store.SetRouting(0, map[string]wanstate.MemberRouting{
		admissionWANID:   {Carrying: true, SelectionEnabled: true, V4Ready: true, V6Ready: true},
		admissionOtherID: {Carrying: true, SelectionEnabled: true, V4Ready: true, V6Ready: true},
	})
	for _, id := range []string{admissionWANID, admissionOtherID} {
		store.SetReadiness(id, "ipv4", "ready")
		store.SetReadiness(id, "ipv6", "ready")
	}
	return fixture
}

func (fixture admissionFixture) preparePaths(t *testing.T) {
	t.Helper()
	for _, entry := range []struct {
		family                                         int
		source, destination, gateway, alternateGateway string
	}{
		{unix.AF_INET, admissionClientV4, admissionRemoteV4, "198.51.100.2", "198.51.101.2"},
		{unix.AF_INET6, admissionClientV6, admissionRemoteV6, "2001:db8:2::2", "2001:db8:3::2"},
	} {
		neighbor := netlink.Neigh{LinkIndex: fixture.output.Index, Family: entry.family, IP: net.ParseIP(entry.gateway), HardwareAddr: fixture.peer.HardwareAddr, State: unix.NUD_PERMANENT}
		if err := netlink.NeighSet(&neighbor); err != nil {
			t.Fatalf("install provider neighbor family %d on index %d for %s: %v", neighbor.Family, neighbor.LinkIndex, neighbor.IP, err)
		}
		fixture.neighbors[entry.family] = neighbor
		for _, neighbor := range []netlink.Neigh{
			{LinkIndex: fixture.other.Index, Family: entry.family, IP: net.ParseIP(entry.alternateGateway), HardwareAddr: fixture.otherPeer.HardwareAddr, State: unix.NUD_PERMANENT},
			{LinkIndex: fixture.input.Index, Family: entry.family, IP: net.ParseIP(entry.source), HardwareAddr: fixture.sender.HardwareAddr, State: unix.NUD_PERMANENT},
		} {
			if err := netlink.NeighSet(&neighbor); err != nil {
				t.Fatalf("install admission neighbor family %d on index %d for %s: %v", neighbor.Family, neighbor.LinkIndex, neighbor.IP, err)
			}
		}
		for _, route := range []netlink.Route{
			{Family: entry.family, Table: unix.RT_TABLE_MAIN, LinkIndex: fixture.other.Index, Gw: net.ParseIP(entry.alternateGateway)},
			{Family: entry.family, Table: admissionRouteTable, LinkIndex: fixture.output.Index, Gw: net.ParseIP(entry.gateway)},
		} {
			if err := netlink.RouteAdd(&route); err != nil {
				t.Fatalf("install admission default route family %d table %d output %d gateway %s: %v", route.Family, route.Table, route.LinkIndex, route.Gw, err)
			}
		}
		rule := netlink.NewRule()
		rule.Family, rule.Priority, rule.Table = entry.family, admissionRoutePriority, admissionRouteTable
		rule.IifName, rule.Mark, rule.IPProto = fixture.input.Name, admissionMark, unix.IPPROTO_TCP
		rule.Sport = netlink.NewRulePortRange(nativeCaptureSourcePort, nativeCaptureSourcePort)
		rule.Dport = netlink.NewRulePortRange(nativeCaptureDestinationPort, nativeCaptureDestinationPort)
		if entry.family == unix.AF_INET {
			rule.Tos = admissionTrafficClass
		}
		if err := netlink.RuleAdd(rule); err != nil {
			t.Fatalf("install admission packet rule family %d table %d mark %d traffic class %#x: %v", rule.Family, rule.Table, rule.Mark, rule.Tos, err)
		}
		selector := netif.PacketSelector{
			Source:      netip.AddrPortFrom(netip.MustParseAddr(entry.source), nativeCaptureSourcePort),
			Destination: netip.AddrPortFrom(netip.MustParseAddr(entry.destination), nativeCaptureDestinationPort),
			Protocol:    unix.IPPROTO_TCP, InputInterface: fixture.input.Index, Mark: admissionMark, TrafficClass: admissionTrafficClass,
		}
		if entry.family == unix.AF_INET6 {
			selector.FlowLabel = nativeCaptureFlowLabel
		}
		path, usable, err := netif.ForwardPathLookup(fixture.ctx, fixture.module.Log, selector)
		if err != nil || !usable || path.Link.Attrs().Index != fixture.output.Index {
			t.Fatalf("marked packet did not select the applied provider route: usable=%v path=%+v error=%v", usable, path, err)
		}
		selector.Mark = 0
		path, usable, err = netif.ForwardPathLookup(fixture.ctx, fixture.module.Log, selector)
		if err != nil || !usable || path.Link.Attrs().Index != fixture.other.Index {
			t.Fatalf("unmarked baseline did not select the other provider: usable=%v path=%+v error=%v", usable, path, err)
		}
		if entry.family == unix.AF_INET {
			selector.Mark = admissionMark
			selector.TrafficClass = 0
			path, usable, err = netif.ForwardPathLookup(fixture.ctx, fixture.module.Log, selector)
			if err != nil || !usable || path.Link.Attrs().Index != fixture.other.Index {
				t.Fatalf("different traffic class did not select the other provider: usable=%v path=%+v error=%v", usable, path, err)
			}
		}
	}
	if err := netlink.NeighSet(&netlink.Neigh{LinkIndex: fixture.input.Index, Family: unix.AF_INET6, IP: net.ParseIP(admissionInternalClientV6), HardwareAddr: fixture.sender.HardwareAddr, State: unix.NUD_PERMANENT}); err != nil {
		t.Fatal(err)
	}
}

func (fixture admissionFixture) publishFirewall(t *testing.T) {
	t.Helper()
	if err := firewall.Apply(fixture.ctx, fixture.rules); err != nil {
		t.Fatalf("apply admission firewall: %v", err)
	}
	if _, err := firewall.Inspect(fixture.ctx, fixture.rules); err != nil {
		t.Fatalf("inspect admission firewall: %v", err)
	}
	for _, id := range []string{admissionWANID, admissionOtherID} {
		fixture.store.SetFirewallProtection(id, "ipv4", "ready")
		fixture.store.SetFirewallProtection(id, "ipv6", "ready")
	}
}

func (fixture admissionFixture) publishObservation(t *testing.T, connection interfaceintent.Connection) {
	t.Helper()
	ctx, cancel := context.WithCancel(fixture.ctx)
	defer cancel()
	monitor := netif.NewMonitor(ctx, fixture.module.Log, netif.MonitorConfig{Iface: connection.Name, ConnectionID: connection.ID.String()})
	for {
		select {
		case event := <-monitor.Events:
			fixture.store.RecordObservation(event)
			state := fixture.store.Snapshot().Connections[connection.ID.String()]
			if state.Observation == "fresh" && state.LinkState == "up" && state.ActualName == connection.Name {
				return
			}
		case <-ctx.Done():
			t.Fatal("kernel monitor did not establish the provider identity")
		}
	}
}

func (fixture admissionFixture) flow(t *testing.T, source, destination, translatedSource string) conntrack.Flow {
	t.Helper()
	status := conntrack.StatusConfirmed | conntrack.StatusSeenReply | conntrack.StatusAssured
	flow := conntrack.NewFlow(unix.IPPROTO_TCP, status, netip.MustParseAddr(source), netip.MustParseAddr(destination), nativeCaptureSourcePort, nativeCaptureDestinationPort, admissionFlowTimeout, admissionMark)
	flow.ProtoInfo.TCP = &conntrack.ProtoInfoTCP{State: conntrackEstablished}
	if translatedSource != "" {
		flow.TupleReply.IP.DestinationAddress = netip.MustParseAddr(translatedSource)
		flow.Status |= conntrack.StatusSrcNAT | conntrack.StatusSrcNATDone
		admissionCreateSNAT(t, fixture.ctx, flow)
	} else {
		if err := fixture.tracking.Create(flow); err != nil {
			t.Fatal(err)
		}
	}
	current, err := fixture.tracking.Get(conntrack.Flow{TupleOrig: flow.TupleOrig})
	if err != nil || current.ID == 0 || current.TupleOrig != flow.TupleOrig || current.TupleReply != flow.TupleReply {
		t.Fatalf("kernel did not confirm the requested connection tuples: %+v error=%v", current, err)
	}
	return current
}

func admissionCreateSNAT(t *testing.T, ctx context.Context, flow conntrack.Flow) {
	t.Helper()
	socket, err := netfilter.Dial(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := socket.Close(); err != nil {
			t.Error(err)
		}
	}()
	deadline, bounded := ctx.Deadline()
	if !bounded {
		t.Fatal("SNAT fixture requires a bounded kernel request")
	}
	if err := socket.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	reply := flow.TupleReply
	reply.IP.DestinationAddress = flow.TupleOrig.IP.SourceAddress
	message, err := netfilter.MarshalNetlink(netfilter.Header{
		SubsystemID: netfilter.NFSubsysCTNetlink, MessageType: admissionCTNew,
		Family: netfilter.ProtoIPv4, Flags: mdnetlink.Request | mdnetlink.Acknowledge | mdnetlink.Create | mdnetlink.Excl,
	}, []netfilter.Attribute{
		admissionConntrackTuple(flow.TupleOrig, admissionCTTupleOriginal),
		admissionConntrackTuple(reply, admissionCTTupleReply),
		{Type: admissionCTStatus, Data: netfilter.Uint32Bytes(uint32(flow.Status))},
		{Type: admissionCTTimeout, Data: netfilter.Uint32Bytes(flow.Timeout)},
		{Type: admissionCTMark, Data: netfilter.Uint32Bytes(flow.Mark)},
		{Type: admissionCTProtocolInfo, Nested: true, Children: []netfilter.Attribute{
			{Type: admissionCTTCPInfo, Nested: true, Children: []netfilter.Attribute{
				{Type: admissionCTTCPState, Data: []byte{conntrackEstablished}},
			}},
		}},
		{Type: admissionCTNATSource, Nested: true, Children: []netfilter.Attribute{
			{Type: admissionCTNATMinimumIP, Data: flow.TupleReply.IP.DestinationAddress.AsSlice()},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := socket.Query(message); err != nil {
		t.Fatalf("kernel did not create the source NAT mapping: %v", err)
	}
}

func admissionConntrackTuple(tuple conntrack.Tuple, kind uint16) netfilter.Attribute {
	return netfilter.Attribute{Type: kind, Nested: true, Children: []netfilter.Attribute{
		{Type: admissionCTTupleIP, Nested: true, Children: []netfilter.Attribute{
			{Type: admissionCTIPv4Source, Data: tuple.IP.SourceAddress.AsSlice()},
			{Type: admissionCTIPv4Destination, Data: tuple.IP.DestinationAddress.AsSlice()},
		}},
		{Type: admissionCTTupleProtocol, Nested: true, Children: []netfilter.Attribute{
			{Type: admissionCTProtocolNumber, Data: []byte{tuple.Proto.Protocol}},
			{Type: admissionCTSourcePort, Data: netfilter.Uint16Bytes(tuple.Proto.SourcePort)},
			{Type: admissionCTDestinationPort, Data: netfilter.Uint16Bytes(tuple.Proto.DestinationPort)},
		}},
	}}
}

func (fixture admissionFixture) requireCandidate(t *testing.T, observation packetObservation, expected conntrack.Flow, translated conntrack.Tuple, direction int, output, neighbor *net.Interface) {
	t.Helper()
	spec, guards, current, wan, eligibility, err := fixture.module.candidate(fixture.ctx, observation)
	if err != nil || eligibility != EligibilityReady {
		t.Fatalf("kernel and Rust evidence did not compose an eligible candidate: decision=%d error=%v", eligibility, err)
	}
	if current.ID != expected.ID || current.TupleOrig != expected.TupleOrig || current.TupleReply != expected.TupleReply || current.Mark != admissionMark || wan.Key() != admissionWANID {
		t.Fatalf("candidate changed the kernel identity or mark-derived provider: flow=%+v provider=%+v", current, wan)
	}
	if spec.Cookie != uint64(current.ID) || spec.PolicyGeneration != fixture.module.policyGeneration.Load() || spec.RequestedDirections != []uint32{1, 2}[direction] {
		t.Fatalf("candidate lost connection, policy or direction identity: %+v", spec)
	}
	mapping := spec.Directions[direction]
	if mapping.Original != observation.Tuple || mapping.Translated != translated || mapping.Origin != fixture.module.links[observation.ifindex].origin || mapping.EgressData != fixture.module.links[output.Index].data {
		t.Fatalf("candidate changed the observed or translated tuple: %+v", mapping)
	}
	if mapping.OriginalSourceMAC != observation.SourceMAC || mapping.OriginalDestinationMAC != observation.DestinationMAC || mapping.OutputSourceMAC != [6]byte(output.HardwareAddr) || mapping.OutputDestinationMAC != [6]byte(neighbor.HardwareAddr) || mapping.OutputMTU != uint16(output.MTU) {
		t.Fatalf("candidate changed current Ethernet or MTU evidence: %+v", mapping)
	}
	if guards[direction].TrafficClass != admissionTrafficClass || guards[direction].FlowLabel != observation.FlowLabel {
		t.Fatalf("candidate changed the Rust packet selectors: %+v", guards[direction])
	}
	if observation.Tuple.IP.SourceAddress.Is6() && guards[direction].FlowLabel != nativeCaptureFlowLabel {
		t.Fatalf("IPv6 candidate omitted the observed flow label: %+v", guards[direction])
	}
	if _, err := offload.FreshConntrackFlow(fixture.ctx, current); err != nil {
		t.Fatalf("candidate identity did not pass the final kernel lookup: %v", err)
	}
}

func (fixture admissionFixture) requireRejected(t *testing.T, observation packetObservation, expected Eligibility) {
	t.Helper()
	_, _, _, _, actual, err := fixture.module.candidate(fixture.ctx, observation)
	if actual != expected {
		t.Fatalf("candidate eligibility=%d, expected %d: %v", actual, expected, err)
	}
}

func admissionVeth(t *testing.T, name, peerName string, suffix byte) (*net.Interface, *net.Interface) {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name, HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, suffix}}, PeerName: peerName, PeerHardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, suffix + 1}}); err != nil {
		t.Fatalf("create admission veth %s and %s: %v", name, peerName, err)
	}
	for _, currentName := range []string{name, peerName} {
		link, err := netlink.LinkByName(currentName)
		if err != nil {
			t.Fatalf("inspect admission veth %s: %v", currentName, err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatalf("enable admission veth %s: %v", currentName, err)
		}
	}
	link, err := net.InterfaceByName(name)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := net.InterfaceByName(peerName)
	if err != nil {
		t.Fatal(err)
	}
	return link, peer
}

func admissionAddress(t *testing.T, link *net.Interface, value string) {
	t.Helper()
	address, err := netlink.ParseAddr(value)
	if err != nil {
		t.Fatal(err)
	}
	address.Flags = unix.IFA_F_NODAD
	current, err := netlink.LinkByIndex(link.Index)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(current, address); err != nil {
		t.Fatalf("install admission address %s on %s: %v", value, link.Name, err)
	}
	addresses, err := netlink.AddrList(current, unix.AF_UNSPEC)
	if err != nil {
		t.Fatalf("inspect admission addresses on %s: %v", link.Name, err)
	}
	for _, actual := range addresses {
		if actual.IPNet.String() == address.IPNet.String() && actual.Flags&unix.IFA_F_TENTATIVE == 0 {
			return
		}
	}
	t.Fatalf("kernel did not confirm address %s", value)
}

func admissionObservation(t *testing.T, sender, input *net.Interface, tuple conntrack.Tuple) packetObservation {
	t.Helper()
	addresses := [2]string{tuple.IP.SourceAddress.String(), tuple.IP.DestinationAddress.String()}
	frame := nativeCaptureFrameSelectors(t, sender, input, addresses, tuple.Proto.Protocol, admissionTrafficClass)
	if tuple.Proto.SourcePort != nativeCaptureSourcePort || tuple.Proto.DestinationPort != nativeCaptureDestinationPort {
		frame = admissionReplyFrame(t, sender, input, tuple)
	}
	observed, err := offload.ObserveNativeFrames([][]byte{frame})
	if err != nil || len(observed) != 1 || !observed[0].Eligible || observed[0].DecodeError != nil || observed[0].Tuple != tuple {
		t.Fatalf("Rust did not validate the Ethernet frame: observation=%+v error=%v", observed, err)
	}
	return packetObservation{NativeObservation: observed[0], ifindex: input.Index, at: time.Now()}
}

func admissionReplyFrame(t *testing.T, sender, input *net.Interface, tuple conntrack.Tuple) []byte {
	t.Helper()
	ethernet := &layers.Ethernet{SrcMAC: sender.HardwareAddr, DstMAC: input.HardwareAddr, EthernetType: layers.EthernetTypeIPv4}
	var network gopacket.NetworkLayer
	var serialized gopacket.SerializableLayer
	if tuple.IP.SourceAddress.Is4() {
		ipv4 := &layers.IPv4{Version: 4, IHL: 5, TTL: 64, TOS: admissionTrafficClass, SrcIP: tuple.IP.SourceAddress.AsSlice(), DstIP: tuple.IP.DestinationAddress.AsSlice(), Protocol: layers.IPProtocolTCP}
		network, serialized = ipv4, ipv4
	} else {
		ethernet.EthernetType = layers.EthernetTypeIPv6
		ipv6 := &layers.IPv6{Version: 6, HopLimit: 64, TrafficClass: admissionTrafficClass, FlowLabel: nativeCaptureFlowLabel, SrcIP: tuple.IP.SourceAddress.AsSlice(), DstIP: tuple.IP.DestinationAddress.AsSlice(), NextHeader: layers.IPProtocolTCP}
		network, serialized = ipv6, ipv6
	}
	tcp := &layers.TCP{SrcPort: layers.TCPPort(tuple.Proto.SourcePort), DstPort: layers.TCPPort(tuple.Proto.DestinationPort), ACK: true, Seq: 1, Ack: 1, Window: 1024}
	if err := tcp.SetNetworkLayerForChecksum(network); err != nil {
		t.Fatal(err)
	}
	buffer := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(buffer, gopacket.SerializeOptions{FixLengths: true, ComputeChecksums: true}, ethernet, serialized, tcp, gopacket.Payload("native admission")); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func admissionPrivateBPFMount(t *testing.T) {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	if err := unix.Unshare(unix.CLONE_NEWNS); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount("mwan-admission-test", admissionPrivateBPFFS, "bpf", 0, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(admissionPrivateBPFFS, unix.MNT_DETACH); err != nil {
			t.Error(err)
		}
	})
}
