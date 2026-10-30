//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/ti-mo/conntrack"
	"github.com/vishvananda/netlink"
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
	nativeDSCPMask           uint8            = 0xfc
)

// Eligibility separates a usable mapping from unsupported or stale evidence.
type Eligibility uint8

const (
	// EligibilityReady permits installation after every required kernel observation agrees.
	EligibilityReady Eligibility = iota
	// EligibilityUnsupported leaves packet classes outside the native contract in Linux.
	EligibilityUnsupported
	// EligibilityStale requires fresh connection or forwarding evidence before another attempt.
	EligibilityStale
	// EligibilityPolicyUnavailable prevents admission before provider policy becomes ready.
	EligibilityPolicyUnavailable
	// EligibilityCapacity preserves Linux forwarding when native storage cannot accept a mapping.
	EligibilityCapacity
)

func (module *Module) admit(ctx context.Context, observation packetObservation) {
	stage := admissionInstalled
	var diagnostic admissionSample
	var diagnosticErr error
	if module.cfg.Settings.ObservationDiagnostics {
		defer func() { module.recordAdmission(stage, observation, &diagnostic, diagnosticErr) }()
	}
	if module.admissionPaused.Load() {
		stage = admissionPaused
		module.stats.ObservationSkipped++
		return
	}
	now := clock.Real{}.Now()
	age := now.Sub(observation.at)
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.age, diagnostic.ageChecked = age, true
	}
	if age < 0 || age > module.cfg.Settings.ObservationMaxAge {
		stage = admissionObservationAge
		module.stats.Fallback++
		return
	}
	if !observation.Eligible {
		stage = admissionObservationIneligible
		module.stats.Fallback++
		return
	}
	allowed, err := module.attempts.admit(observation, now, module.cfg.Settings.CounterInterval)
	if err != nil {
		stage, diagnosticErr = admissionAttemptFailed, err
		_ = module.fail(ctx, err)
		return
	}
	if !allowed {
		stage = admissionAttemptThrottled
		return
	}
	evidence := module.evidenceFor(module.currentEvidenceEpoch())
	requestContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.RequestTimeout)
	defer cancel()
	requestContext, expire := context.WithDeadline(requestContext, observation.at.Add(module.cfg.Settings.ObservationMaxAge))
	defer expire()
	spec, guards, flow, wan, decision, err := module.candidate(requestContext, observation)
	if err != nil || decision != EligibilityReady {
		stage, diagnosticErr = admissionCandidateRejected, err
		if module.cfg.Settings.ObservationDiagnostics {
			diagnostic.eligibility, diagnostic.eligibilityValid = decision, true
		}
		module.stats.Fallback++
		return
	}
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.connection, diagnostic.wan = freezeAdmissionConnection(flow), wan.Key()
		diagnostic.eligibility, diagnostic.eligibilityValid = decision, true
	}
	identity := flowIdentity(flow)
	if module.observeTrackedSelectors(ctx, requestContext, identity, observation, guards, evidence) {
		stage = admissionTrackedHandled
		return
	}
	entry := newAdmittedFlow(flow, spec, guards, wan, observation, now)
	if module.resumeCandidate(ctx, requestContext, identity, entry, evidence) {
		stage = admissionResumptionHandled
		return
	}
	stage, diagnosticErr = module.installCandidate(ctx, requestContext, identity, entry, evidence, &diagnostic)
}

func (module *Module) installCandidate(ctx, requestContext context.Context, identity connectionIdentity, entry *admittedFlow, evidence *evidenceCheck, diagnostic *admissionSample) (admissionStage, error) {
	retirement, err := module.runtime.Retirement(requestContext)
	if err != nil {
		module.stats.Fallback++
		return admissionRetirementFailed, &nativeOperationError{operation: "query native retirement storage", connectionID: identity.id, ifindex: 0, cause: err}
	}
	requireShared := retirement.RetainedPairs >= retirement.RetainedPairCapacity && retirement.ReusableSharedSlots == 0
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.retirement, diagnostic.retirementValid = retirement, true
		diagnostic.requireShared, diagnostic.tracked, diagnostic.capacity = requireShared, len(module.flows), module.cfg.Settings.PairCapacity
	}
	if requireShared || len(module.flows) >= int(module.cfg.Settings.PairCapacity) {
		if !module.evictLRU(requestContext, requireShared) {
			module.stats.Fallback++
			return admissionStorageRejected, module.stats.Failure
		}
	}
	counterStarted := clock.Real{}.Now()
	current, err := offload.FreshConntrackFlow(requestContext, entry.flow)
	if err != nil {
		module.stats.Fallback++
		return admissionFreshLookupFailed, &nativeOperationError{operation: "refresh native candidate connection", connectionID: identity.id, ifindex: 0, cause: err}
	}
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.currentConnection = freezeAdmissionConnection(current)
	}
	if !sameConnectionPolicy(entry.flow, current) {
		module.stats.Fallback++
		return admissionFreshPolicyMismatch, nil
	}
	ageAfterLookup := (clock.Real{}).Now().Sub(entry.observation.at)
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.age = ageAfterLookup
	}
	if ageAfterLookup > module.cfg.Settings.ObservationMaxAge {
		module.stats.Fallback++
		return admissionObservationExpired, nil
	}
	if err := evidence.guard.Check(); err != nil {
		module.stats.Fallback++
		module.invalidateEvidence(ctx)
		return admissionEvidenceChanged, &nativeOperationError{operation: "verify native admission evidence", connectionID: identity.id, ifindex: 0, cause: err}
	}
	result, err := module.runtime.InstallObservedWithEvidence(requestContext, current, entry.spec, entry.guards, &evidence.guard)
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.installation, diagnostic.installationValid, diagnostic.requestedDirections = result, true, entry.spec.RequestedDirections
	}
	stage := installationStage(result, entry.spec.RequestedDirections, err)
	module.retainInstallation(identity, entry, current, result, counterStarted)
	module.completeInstallation(ctx, identity, entry.spec, result, err, evidence)
	if err != nil {
		return stage, &nativeOperationError{operation: "install native candidate", connectionID: identity.id, ifindex: 0, cause: err}
	}
	return stage, nil
}

func (module *Module) retainInstallation(identity connectionIdentity, entry *admittedFlow, current conntrack.Flow, result offload.NativePairResult, counterStarted time.Time) {
	if result.RestorationDirections != 0 || result.InstalledDirections != 0 || result.CleanupFailedDirections != 0 {
		entry.flow, entry.timeout, entry.result = current, current.Timeout, result
		module.startCounters(entry, counterStarted)
		module.flows[identity] = entry
	}
}

func newAdmittedFlow(flow conntrack.Flow, spec offload.NativePairSpec, guards [2]offload.NativePacketGuard, wan WAN, observation packetObservation, now time.Time) *admittedFlow {
	var result offload.NativePairResult
	return &admittedFlow{
		flow: flow, spec: spec, guards: guards, result: result,
		uncertain: false, wan: wan.Key(), timeout: flow.Timeout,
		counters: offload.NewNativeCounterHistory(now, 0), observation: observation,
		selectorAttemptAt: time.Time{}, selectorsPending: false,
	}
}

func (module *Module) resumeCandidate(ctx, requestContext context.Context, identity connectionIdentity, candidate *admittedFlow, evidence *evidenceCheck) bool {
	entry, retired := module.resumeCandidates[identity]
	if !retired {
		return false
	}
	if !sameConnectionPolicy(entry.flow, candidate.flow) || !sameNativeMapping(entry.spec, candidate.spec) || candidate.wan != entry.wan || len(module.flows) >= int(module.cfg.Settings.PairCapacity) {
		module.stats.Fallback++
		return true
	}
	module.flows[identity] = entry
	delete(module.resumeCandidates, identity)
	if candidate.guards != entry.guards || entry.selectorsPending {
		module.reobserveEntry(ctx, requestContext, identity, entry, candidate.observation, evidence)
		return true
	}
	entry.observation, entry.counters = candidate.observation, candidate.counters
	module.resumeEntry(ctx, requestContext, identity, entry, evidence)
	if evidence.guard.Check() != nil {
		module.invalidateEvidence(ctx)
	}
	return true
}

func (module *Module) completeInstallation(ctx context.Context, identity connectionIdentity, spec offload.NativePairSpec, result offload.NativePairResult, err error, evidence *evidenceCheck) {
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
	module.logAdmittedFlow(ctx, "native rule installed", module.flows[identity])
	if evidence.guard.Check() != nil {
		module.invalidateEvidence(ctx)
	}
}

func (module *Module) candidate(ctx context.Context, observation packetObservation) (resultSpec offload.NativePairSpec, resultGuards [2]offload.NativePacketGuard, resultFlow conntrack.Flow, resultWAN WAN, resultEligibility Eligibility, resultErr error) {
	stage := candidateReady
	var diagnostic admissionSample
	connectionAvailable := false
	if module.cfg.Settings.ObservationDiagnostics {
		defer func() {
			if connectionAvailable {
				diagnostic.connection = freezeAdmissionConnection(resultFlow)
			}
			diagnostic.wan, diagnostic.eligibility = resultWAN.Key(), resultEligibility
			diagnostic.eligibilityValid = true
			module.recordAdmission(stage, observation, &diagnostic, resultErr)
		}()
	}
	var spec offload.NativePairSpec
	var guards [2]offload.NativePacketGuard
	var emptyFlow conntrack.Flow
	var wan WAN
	input, stage := module.candidateInput(observation)
	if stage != candidateReady {
		return spec, guards, emptyFlow, wan, EligibilityUnsupported, nil
	}
	if err := module.verifyLinks(); err != nil {
		stage = candidateLinksChanged
		return spec, guards, emptyFlow, wan, EligibilityStale, err
	}
	snapshot := module.Env.LiveState.Snapshot()
	kernelTuple, supported := module.ingressTuple(snapshot, input, observation.Tuple)
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.kernelTuple, diagnostic.kernelTupleValid = kernelTuple, true
	}
	if !supported {
		stage = candidateIngressTranslation
		return spec, guards, emptyFlow, wan, EligibilityUnsupported, nil
	}
	flow, err := offload.LookupConntrackFlow(ctx, kernelTuple, 0)
	if err != nil {
		stage = candidateConntrackLookup
		module.Log.WarnContext(ctx, "native candidate conntrack lookup failed", "err", err)
		return spec, guards, emptyFlow, wan, EligibilityStale, fmt.Errorf("look up native candidate connection: %w", err)
	}
	connectionAvailable = true
	if !connectionEligible(flow) {
		stage = candidateConnectionIneligible
		return spec, guards, flow, wan, EligibilityUnsupported, nil
	}
	wan, ready := module.candidateWAN(snapshot, flow, kernelTuple.IP.SourceAddress.Is4())
	if !ready {
		stage = candidateWANNotReady
		return spec, guards, flow, wan, EligibilityPolicyUnavailable, nil
	}
	if err := module.inspectPolicy(ctx); err != nil {
		stage = candidatePolicyRejected
		module.Log.DebugContext(ctx, "native candidate policy is ineligible", "connection_id", flow.ID, "wan", wan.Key(), "err", err)
		return spec, guards, flow, wan, EligibilityPolicyUnavailable, err
	}
	spec, guards, stage, decision, err := module.candidateMapping(ctx, observation, input, snapshot, flow, wan, kernelTuple, &diagnostic)
	return spec, guards, flow, wan, decision, err
}

func (module *Module) candidateMapping(ctx context.Context, observation packetObservation, input boundLink, snapshot wanstate.Snapshot, flow conntrack.Flow, wan WAN, kernelTuple conntrack.Tuple, diagnostic *admissionSample) (offload.NativePairSpec, [2]offload.NativePacketGuard, admissionStage, Eligibility, error) {
	var spec offload.NativePairSpec
	var guards [2]offload.NativePacketGuard
	translated, index, decision := candidateDirection(flow, kernelTuple)
	if decision != EligibilityReady {
		return spec, guards, candidateTupleDirection, decision, nil
	}
	selector := netif.PacketSelector{
		Source:      netip.AddrPortFrom(kernelTuple.IP.SourceAddress, kernelTuple.Proto.SourcePort),
		Destination: netip.AddrPortFrom(translated.IP.DestinationAddress, translated.Proto.DestinationPort),
		Protocol:    kernelTuple.Proto.Protocol, InputInterface: observation.ifindex,
		Mark: flow.Mark, TrafficClass: observation.TrafficClass, FlowLabel: observation.FlowLabel,
	}
	path, usable, err := netif.ForwardPathLookup(ctx, module.Log, selector)
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.route = freezeAdmissionRoute(selector, path, usable)
	}
	if err != nil {
		module.Log.WarnContext(ctx, "native candidate forwarding lookup failed", "err", err)
		return spec, guards, candidateRouteLookup, EligibilityUnsupported, fmt.Errorf("resolve native candidate forwarding path: %w", err)
	}
	if !usable {
		return spec, guards, candidateRouteUnusable, EligibilityUnsupported, nil
	}
	output, bound := module.links[path.Link.Attrs().Index]
	if !bound {
		return spec, guards, candidateOutputUnbound, EligibilityUnsupported, nil
	}
	if !module.pathAllowed(input, output, wan, kernelTuple.IP.SourceAddress.Is4()) {
		return spec, guards, candidatePathRejected, EligibilityUnsupported, nil
	}
	if kernelTuple.IP.SourceAddress.Is4() {
		if !module.ipv4MappingAllowed(wan, flow) {
			return spec, guards, candidateIPv4Mapping, EligibilityUnsupported, nil
		}
	} else if output.connection.ID.String() == wan.Key() {
		var supported bool
		translated, supported = translateSource(snapshot, wan.Key(), translated)
		if !supported {
			return spec, guards, candidateIPv6Translation, EligibilityUnsupported, nil
		}
	}
	mtu := output.kernel.MTU
	if path.Route.MTU > 0 {
		mtu = min(mtu, path.Route.MTU)
	}
	outputMTU, err := checkedNativeMTU(mtu)
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.outputMTU, diagnostic.mtuChecked = mtu, true
	}
	if err != nil {
		module.Log.WarnContext(ctx, "native candidate MTU is invalid", "mtu", mtu, "err", err)
		return spec, guards, candidateMTUInvalid, EligibilityUnsupported, fmt.Errorf("check native candidate output MTU: %w", err)
	}
	if observation.NetworkLength > uint32(outputMTU) {
		return spec, guards, candidateLengthAboveMTU, EligibilityUnsupported, nil
	}
	stage := module.candidatePolicyGeneration(snapshot, wan, kernelTuple.IP.SourceAddress.Is4(), diagnostic)
	if stage != candidateReady {
		return spec, guards, stage, EligibilityStale, nil
	}
	mask := [2]uint32{1, 2}
	spec.Cookie = uint64(flow.ID)
	spec.PolicyGeneration = module.policyGeneration.Load()
	spec.RequestedDirections = mask[index]
	spec.Directions[index] = offload.NativeDirection{
		Origin: input.origin, EgressData: output.data,
		Original: observation.Tuple, Translated: translated,
		OriginalSourceMAC: observation.SourceMAC, OriginalDestinationMAC: observation.DestinationMAC,
		OutputSourceMAC: [6]byte(path.Link.Attrs().HardwareAddr), OutputDestinationMAC: [6]byte(path.Neighbor.HardwareAddr), OutputMTU: outputMTU,
	}
	guards[index] = offload.NativePacketGuard{TrafficClass: observation.TrafficClass & nativeDSCPMask, FlowLabel: observation.FlowLabel}
	return spec, guards, candidateReady, EligibilityReady, nil
}

func (module *Module) candidateInput(observation packetObservation) (boundLink, admissionStage) {
	input, bound := module.links[observation.ifindex]
	if !bound {
		return input, candidateIngressUnbound
	}
	if !input.hasOrigin {
		return input, candidateOriginAbsent
	}
	if observation.DestinationMAC != [6]byte(input.kernel.HardwareAddr) {
		return input, candidateDestinationMAC
	}
	return input, candidateReady
}

func (module *Module) candidatePolicyGeneration(snapshot wanstate.Snapshot, wan WAN, ipv4 bool, diagnostic *admissionSample) admissionStage {
	current := module.Env.LiveState.Snapshot()
	generationChanged := current.NPTPolicyGeneration != snapshot.NPTPolicyGeneration || current.RoutingGeneration != snapshot.RoutingGeneration
	policyReady := false
	if !generationChanged {
		policyReady = module.policyReady(current, wan, ipv4)
	}
	if module.cfg.Settings.ObservationDiagnostics {
		diagnostic.generationChecked = true
		diagnostic.nptBefore, diagnostic.nptAfter = snapshot.NPTPolicyGeneration, current.NPTPolicyGeneration
		diagnostic.routingBefore, diagnostic.routingAfter = snapshot.RoutingGeneration, current.RoutingGeneration
		diagnostic.policyChecked, diagnostic.policyReady = !generationChanged, policyReady
	}
	if generationChanged {
		return candidateGenerationChanged
	}
	if !policyReady {
		return candidatePolicyBecameUnready
	}
	return candidateReady
}

func candidateDirection(flow conntrack.Flow, kernelTuple conntrack.Tuple) (conntrack.Tuple, int, Eligibility) {
	index := 0
	translated := reverseTuple(flow.TupleReply)
	if kernelTuple == flow.TupleReply {
		index = 1
		translated = reverseTuple(flow.TupleOrig)
	} else if kernelTuple != flow.TupleOrig {
		return translated, index, EligibilityStale
	}
	if kernelTuple.IP.SourceAddress.Is6() {
		if reverseTuple(flow.TupleReply) != flow.TupleOrig {
			return translated, index, EligibilityUnsupported
		}
		translated = kernelTuple
	}
	return translated, index, EligibilityReady
}

func (module *Module) candidateWAN(snapshot wanstate.Snapshot, flow conntrack.Flow, ipv4 bool) (WAN, bool) {
	var wan WAN
	for _, member := range module.cfg.WANs {
		if member.Mark != flow.Mark {
			continue
		}
		if wan.Key() != "" {
			return wan, false
		}
		wan = member
	}
	return wan, wan.Key() != "" && module.policyReady(snapshot, wan, ipv4)
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
	if wan.TranslationV4.Mode != config.TranslationNAPT44 || original.Proto.Protocol != unix.IPPROTO_TCP && original.Proto.Protocol != unix.IPPROTO_UDP {
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
