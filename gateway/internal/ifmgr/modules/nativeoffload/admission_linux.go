//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	"github.com/agoodkind/netlink"
	"github.com/ti-mo/conntrack"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/nptpolicy"
	"goodkind.io/mwan/internal/offload"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	conntrackEstablished                      = 3
	conntrackHardwareOffload conntrack.Status = 0x8000
)

// Eligibility separates a usable mapping from unsupported or stale evidence.
type Eligibility uint8

const (
	EligibilityReady Eligibility = iota
	EligibilityUnsupported
	EligibilityStale
	EligibilityPolicyUnavailable
	EligibilityCapacity
)

func (module *Module) admit(ctx context.Context, observation packetObservation) {
	if module.admissionPaused.Load() {
		module.stats.ObservationSkipped++
		return
	}
	now := clock.Real{}.Now()
	age := now.Sub(observation.at)
	if age < 0 || age > module.cfg.Settings.ObservationMaxAge || !observation.Eligible {
		module.stats.Fallback++
		return
	}
	if !module.attempts.admit(observation, now, module.cfg.Settings.CounterInterval) {
		return
	}
	evidence := module.evidenceFor(module.currentEvidenceEpoch())
	requestContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.RequestTimeout)
	defer cancel()
	requestContext, expire := context.WithDeadline(requestContext, observation.at.Add(module.cfg.Settings.ObservationMaxAge))
	defer expire()
	spec, guards, flow, wan, decision, err := module.candidate(requestContext, observation)
	if err != nil || decision != EligibilityReady {
		module.stats.Fallback++
		return
	}
	identity := flowIdentity(flow)
	if _, exists := module.flows[identity]; exists {
		return
	}
	if entry, retired := module.resumeCandidates[identity]; retired {
		if !sameConnectionPolicy(entry.flow, flow) || !sameNativeMapping(entry.spec, spec) || guards != entry.guards || len(module.flows) >= int(module.cfg.Settings.PairCapacity) {
			module.stats.Fallback++
			return
		}
		module.flows[identity] = entry
		delete(module.resumeCandidates, identity)
		entry.observation, entry.lastActive = observation, now
		module.resumeEntry(ctx, requestContext, identity, entry, evidence)
		if evidence.guard.Check() != nil {
			module.invalidateEvidence(ctx)
		}
		return
	}
	retirement, err := module.runtime.Retirement(requestContext)
	if err != nil {
		module.stats.Fallback++
		return
	}
	if retirement.RetainedPairs >= retirement.RetainedPairCapacity && uint32(len(module.flows))+retirement.DrainingPairs >= retirement.ActivePairCapacity {
		module.stats.Fallback++
		return
	}
	if len(module.flows) >= int(module.cfg.Settings.PairCapacity) {
		if !module.evictLRU(requestContext) {
			module.stats.Fallback++
			return
		}
	}
	current, err := offload.FreshConntrackFlow(requestContext, flow)
	if err != nil || !sameConnectionPolicy(flow, current) {
		module.stats.Fallback++
		return
	}
	if (clock.Real{}).Now().Sub(observation.at) > module.cfg.Settings.ObservationMaxAge {
		module.stats.Fallback++
		return
	}
	if evidence.guard.Check() != nil {
		module.stats.Fallback++
		module.invalidateEvidence(ctx)
		return
	}
	result, err := module.runtime.InstallObservedWithEvidence(requestContext, current, spec, guards, &evidence.guard)
	if result.RestorationDirections != 0 || result.InstalledDirections != 0 || result.CleanupFailedDirections != 0 {
		module.flows[identity] = &admittedFlow{flow: current, spec: spec, guards: guards, result: result, wan: wan.Key(), timeout: current.Timeout, lastActive: now, observation: observation}
	}
	if err != nil {
		module.stats.Fallback++
		if _, retained := module.flows[identity]; retained {
			if removeErr := module.retire(ctx, identity, "partial native installation"); removeErr != nil {
				_ = module.fail(ctx, fmt.Errorf("retire partial native admission: %w", removeErr))
			}
		}
		return
	}
	if result.State != offload.NativePairInstalled || result.InstalledDirections != spec.RequestedDirections {
		_ = module.fail(ctx, fmt.Errorf("native install did not confirm every requested direction"))
		return
	}
	module.stats.Installed++
	if evidence.guard.Check() != nil {
		module.invalidateEvidence(ctx)
	}
}

func (module *Module) candidate(ctx context.Context, observation packetObservation) (offload.NativePairSpec, [2]offload.NativePacketGuard, conntrack.Flow, WAN, Eligibility, error) {
	var spec offload.NativePairSpec
	var guards [2]offload.NativePacketGuard
	var emptyFlow conntrack.Flow
	var wan WAN
	input, bound := module.links[observation.ifindex]
	if !bound || !input.hasOrigin || observation.DestinationMAC != [6]byte(input.kernel.HardwareAddr) {
		return spec, guards, emptyFlow, wan, EligibilityUnsupported, nil
	}
	if err := module.verifyLinks(); err != nil {
		return spec, guards, emptyFlow, wan, EligibilityStale, err
	}
	snapshot := module.Env.LiveState.Snapshot()
	kernelTuple, supported := module.ingressTuple(snapshot, input, observation.Tuple)
	if !supported {
		return spec, guards, emptyFlow, wan, EligibilityUnsupported, nil
	}
	flow, err := offload.LookupConntrackFlow(ctx, kernelTuple, 0)
	if err != nil {
		return spec, guards, emptyFlow, wan, EligibilityStale, err
	}
	if !connectionEligible(flow) {
		return spec, guards, flow, wan, EligibilityUnsupported, nil
	}
	for _, member := range module.cfg.WANs {
		if member.Mark == flow.Mark {
			if wan.Key() != "" {
				return spec, guards, flow, wan, EligibilityPolicyUnavailable, nil
			}
			wan = member
		}
	}
	if wan.Key() == "" || !module.policyReady(snapshot, wan, kernelTuple.IP.SourceAddress.Is4()) {
		return spec, guards, flow, wan, EligibilityPolicyUnavailable, nil
	}
	index := 0
	translated := reverseTuple(flow.TupleReply)
	if kernelTuple == flow.TupleReply {
		index = 1
		translated = reverseTuple(flow.TupleOrig)
	} else if kernelTuple != flow.TupleOrig {
		return spec, guards, flow, wan, EligibilityStale, nil
	}
	if kernelTuple.IP.SourceAddress.Is6() {
		if reverseTuple(flow.TupleReply) != flow.TupleOrig {
			return spec, guards, flow, wan, EligibilityUnsupported, nil
		}
		translated = kernelTuple
	}
	selector := netif.PacketSelector{
		Source:      netip.AddrPortFrom(kernelTuple.IP.SourceAddress, kernelTuple.Proto.SourcePort),
		Destination: netip.AddrPortFrom(translated.IP.DestinationAddress, translated.Proto.DestinationPort),
		Protocol:    kernelTuple.Proto.Protocol, InputInterface: observation.ifindex,
		Mark: flow.Mark, TrafficClass: observation.TrafficClass, FlowLabel: observation.FlowLabel,
	}
	path, usable, err := netif.ForwardPathLookup(ctx, module.Log, selector)
	if err != nil || !usable {
		return spec, guards, flow, wan, EligibilityUnsupported, err
	}
	output, bound := module.links[path.Link.Attrs().Index]
	if !bound || !module.pathAllowed(input, output, wan, kernelTuple.IP.SourceAddress.Is4()) {
		return spec, guards, flow, wan, EligibilityUnsupported, nil
	}
	if kernelTuple.IP.SourceAddress.Is4() {
		if !module.ipv4MappingAllowed(wan, flow) {
			return spec, guards, flow, wan, EligibilityUnsupported, nil
		}
	} else if output.connection.ID.String() == wan.Key() {
		translated, supported = translateSource(snapshot, wan.Key(), translated)
		if !supported {
			return spec, guards, flow, wan, EligibilityUnsupported, nil
		}
	}
	mtu := output.kernel.MTU
	if path.Route.MTU > 0 {
		mtu = min(mtu, path.Route.MTU)
	}
	if observation.NetworkLength > uint32(mtu) {
		return spec, guards, flow, wan, EligibilityUnsupported, nil
	}
	current := module.Env.LiveState.Snapshot()
	if current.NPTPolicyGeneration != snapshot.NPTPolicyGeneration || current.RoutingGeneration != snapshot.RoutingGeneration || !module.policyReady(current, wan, kernelTuple.IP.SourceAddress.Is4()) {
		return spec, guards, flow, wan, EligibilityStale, nil
	}
	mask := [2]uint32{1, 2}
	spec.Cookie = uint64(flow.ID)
	spec.PolicyGeneration = module.policyGeneration.Load()
	spec.RequestedDirections = mask[index]
	spec.Directions[index] = offload.NativeDirection{
		Origin: input.origin, EgressData: output.data,
		Original: observation.Tuple, Translated: translated,
		OriginalSourceMAC: observation.SourceMAC, OriginalDestinationMAC: observation.DestinationMAC,
		OutputSourceMAC: [6]byte(path.Link.Attrs().HardwareAddr), OutputDestinationMAC: [6]byte(path.Neighbor.HardwareAddr), OutputMTU: uint16(mtu),
	}
	guards[index] = offload.NativePacketGuard{TrafficClass: observation.TrafficClass, FlowLabel: observation.FlowLabel}
	return spec, guards, flow, wan, EligibilityReady, nil
}

func connectionEligible(flow conntrack.Flow) bool {
	required := conntrack.StatusConfirmed | conntrack.StatusSeenReply | conntrack.StatusAssured
	unsupported := conntrack.StatusExpected | conntrack.StatusSeqAdjust | conntrack.StatusDying | conntrack.StatusTemplate | conntrack.StatusUntracked | conntrack.StatusHelper | conntrack.StatusOffload | conntrack.StatusFixedTimeout | conntrackHardwareOffload
	if flow.ID == 0 || flow.Zone != 0 || flow.Timeout == 0 || flow.Status&required != required || flow.Mark == 0 || flow.TupleOrig.Zone != 0 || flow.TupleReply.Zone != 0 {
		return false
	}
	if flow.Status&unsupported != 0 {
		return false
	}
	switch flow.TupleOrig.Proto.Protocol {
	case unix.IPPROTO_TCP:
		return flow.ProtoInfo.TCP != nil && flow.ProtoInfo.TCP.State == conntrackEstablished
	case unix.IPPROTO_UDP:
		return true
	default:
		return false
	}
}

func sameConnectionPolicy(previous, current conntrack.Flow) bool {
	return flowIdentity(previous) == flowIdentity(current) && previous.Mark == current.Mark && connectionEligible(current)
}

func reverseTuple(tuple conntrack.Tuple) conntrack.Tuple {
	tuple.IP.SourceAddress, tuple.IP.DestinationAddress = tuple.IP.DestinationAddress, tuple.IP.SourceAddress
	tuple.Proto.SourcePort, tuple.Proto.DestinationPort = tuple.Proto.DestinationPort, tuple.Proto.SourcePort
	return tuple
}

func (module *Module) policyReady(snapshot wanstate.Snapshot, wan WAN, ipv4 bool) bool {
	routing := snapshot.Routing[wan.Key()]
	translation := snapshot.Translation[wan.Key()]
	connection := snapshot.Connections[wan.Key()]
	if connection.Observation != "fresh" || connection.LinkState != "up" || connection.ActualName != wan.Iface || !routing.Carrying {
		return false
	}
	link, bound := module.links[connection.IfIndex]
	if !bound || link.connection.ID.String() != wan.Key() {
		return false
	}
	if ipv4 {
		return routing.V4Ready && translation.V4.Ready && connection.IPv4.Firewall == "ready" && connection.IPv4.Readiness == "ready"
	}
	if translation.V6.Mode != string(config.TranslationNative) && translation.V6.Mode != string(config.TranslationNPTv6) {
		return false
	}
	if translation.V6.Mode == string(config.TranslationNPTv6) {
		if _, applied := snapshot.NPTPolicies[wan.Key()]; !applied {
			return false
		}
	}
	return routing.V6Ready && translation.V6.Ready && connection.IPv6.Firewall == "ready" && connection.IPv6.Readiness == "ready"
}

func (module *Module) pathAllowed(input, output boundLink, wan WAN, ipv4 bool) bool {
	if input.kernel.Index == output.kernel.Index {
		return false
	}
	for _, path := range module.cfg.Firewall.Paths {
		if ipv4 && !path.IPv4 || !ipv4 && !path.IPv6 {
			continue
		}
		outbound := input.kernel.Name == path.InternalInterface && output.kernel.Name == path.ExternalInterface && output.connection.ID.String() == wan.Key()
		inbound := input.kernel.Name == path.ExternalInterface && output.kernel.Name == path.InternalInterface && input.connection.ID.String() == wan.Key()
		if outbound || inbound {
			return true
		}
	}
	return false
}

func (module *Module) ingressTuple(snapshot wanstate.Snapshot, input boundLink, tuple conntrack.Tuple) (conntrack.Tuple, bool) {
	if tuple.IP.SourceAddress.Is4() {
		return tuple, true
	}
	if input.connection.Roles&interfaceintent.RoleInternal != 0 {
		for _, pair := range snapshot.NPTPolicies {
			if pair.External.Contains(tuple.IP.DestinationAddress) && !slices.Contains(pair.DestinationExceptions, tuple.IP.DestinationAddress) {
				return tuple, false
			}
		}
		return tuple, true
	}
	pair, translated := snapshot.NPTPolicies[input.connection.ID.String()]
	if !translated || !pair.External.Contains(tuple.IP.DestinationAddress) || slices.Contains(pair.DestinationExceptions, tuple.IP.DestinationAddress) {
		return tuple, true
	}
	address, err := nptpolicy.TranslateAddress(pair, tuple.IP.DestinationAddress, false)
	if err != nil {
		return tuple, false
	}
	tuple.IP.DestinationAddress = address
	return tuple, true
}

func translateSource(snapshot wanstate.Snapshot, wan string, tuple conntrack.Tuple) (conntrack.Tuple, bool) {
	pair, translated := snapshot.NPTPolicies[wan]
	if !translated || !pair.Internal.Contains(tuple.IP.SourceAddress) || slices.Contains(pair.SourceExceptions, tuple.IP.SourceAddress) {
		return tuple, true
	}
	address, err := nptpolicy.TranslateAddress(pair, tuple.IP.SourceAddress, true)
	if err != nil {
		return tuple, false
	}
	tuple.IP.SourceAddress = address
	return tuple, true
}

func (module *Module) ipv4MappingAllowed(wan WAN, flow conntrack.Flow) bool {
	if wan.TranslationV4 == nil {
		return false
	}
	original, translated := flow.TupleOrig, reverseTuple(flow.TupleReply)
	if wan.TranslationV4.Mode == config.TranslationNative {
		return original == translated
	}
	if wan.TranslationV4.Mode != config.TranslationNAPT44 || original.Proto.Protocol != unix.IPPROTO_TCP {
		return false
	}
	if original.IP.DestinationAddress != translated.IP.DestinationAddress {
		matched := false
		for _, mapping := range wan.TranslationV4.StaticMappings {
			matched = matched || mapping.External == original.IP.DestinationAddress && mapping.Internal == translated.IP.DestinationAddress
		}
		if !matched || original.Proto.DestinationPort != translated.Proto.DestinationPort {
			return false
		}
	}
	if original.IP.SourceAddress == translated.IP.SourceAddress {
		return original.Proto.SourcePort == translated.Proto.SourcePort
	}
	for _, mapping := range wan.TranslationV4.StaticMappings {
		if mapping.Internal == original.IP.SourceAddress {
			return mapping.External == translated.IP.SourceAddress && original.Proto.SourcePort == translated.Proto.SourcePort
		}
	}
	if !module.cfg.Firewall.InternalNetworkIPv4.Contains(original.IP.SourceAddress) {
		return false
	}
	link, err := netlink.LinkByName(wan.Iface)
	if err != nil {
		return false
	}
	addresses, err := netlink.AddrList(link, unix.AF_INET)
	if err != nil {
		return false
	}
	for _, address := range addresses {
		if address.IP.Equal(translated.IP.SourceAddress.AsSlice()) {
			return true
		}
	}
	return false
}
