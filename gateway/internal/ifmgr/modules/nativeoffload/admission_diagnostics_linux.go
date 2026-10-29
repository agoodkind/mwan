//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"github.com/ti-mo/conntrack"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/offload"
)

type admissionStage uint8

const (
	admissionNotReady admissionStage = iota
	admissionPaused
	admissionObservationAge
	admissionObservationIneligible
	admissionAttemptFailed
	admissionAttemptThrottled
	admissionCandidateRejected
	admissionTrackedHandled
	admissionResumptionHandled
	admissionRetirementFailed
	admissionStorageRejected
	admissionFreshLookupFailed
	admissionFreshPolicyMismatch
	admissionObservationExpired
	admissionEvidenceChanged
	admissionInstallationFailed
	admissionInstallationIncomplete
	admissionInstalled
	candidateIngressUnbound
	candidateOriginAbsent
	candidateDestinationMAC
	candidateLinksChanged
	candidateIngressTranslation
	candidateConntrackLookup
	candidateConnectionIneligible
	candidateWANNotReady
	candidatePolicyRejected
	candidateTupleDirection
	candidateRouteLookup
	candidateRouteUnusable
	candidateOutputUnbound
	candidatePathRejected
	candidateIPv4Mapping
	candidateIPv6Translation
	candidateMTUInvalid
	candidateLengthAboveMTU
	candidateGenerationChanged
	candidatePolicyBecameUnready
	candidateReady
	admissionStageCount
)

func (stage admissionStage) String() string {
	stages := [admissionStageCount]string{
		"not_ready", "paused", "observation_age", "observation_ineligible",
		"attempt_failed", "attempt_throttled", "candidate_rejected", "tracked_handled", "resumption_handled",
		"retirement_failed", "storage_rejected", "fresh_lookup_failed", "fresh_policy_mismatch",
		"observation_expired", "evidence_changed", "installation_failed", "installation_incomplete", "installed",
		"ingress_unbound", "origin_absent", "destination_mac_mismatch", "links_changed", "ingress_translation_rejected",
		"conntrack_lookup_failed", "connection_ineligible", "wan_not_ready", "policy_rejected", "tuple_direction_rejected",
		"route_lookup_failed", "route_unusable", "output_unbound", "path_rejected", "ipv4_mapping_rejected", "ipv6_translation_rejected",
		"mtu_invalid", "length_above_mtu", "generation_changed", "policy_became_unready", "candidate_ready",
	}
	return stages[stage]
}

type admissionOutcome string

const (
	admissionRejected admissionOutcome = "rejected"
	admissionHandled  admissionOutcome = "handled"
	admissionAccepted admissionOutcome = "accepted"
)

func (stage admissionStage) outcome() admissionOutcome {
	switch stage {
	case admissionTrackedHandled, admissionResumptionHandled:
		return admissionHandled
	case admissionInstalled, candidateReady:
		return admissionAccepted
	case admissionNotReady, admissionPaused, admissionObservationAge, admissionObservationIneligible,
		admissionAttemptFailed, admissionAttemptThrottled, admissionCandidateRejected, admissionRetirementFailed,
		admissionStorageRejected, admissionFreshLookupFailed, admissionFreshPolicyMismatch, admissionObservationExpired,
		admissionEvidenceChanged, admissionInstallationFailed, admissionInstallationIncomplete,
		candidateIngressUnbound, candidateOriginAbsent, candidateDestinationMAC, candidateLinksChanged,
		candidateIngressTranslation, candidateConntrackLookup, candidateConnectionIneligible, candidateWANNotReady,
		candidatePolicyRejected, candidateTupleDirection, candidateRouteLookup, candidateRouteUnusable,
		candidateOutputUnbound, candidatePathRejected, candidateIPv4Mapping, candidateIPv6Translation,
		candidateMTUInvalid, candidateLengthAboveMTU, candidateGenerationChanged, candidatePolicyBecameUnready,
		admissionStageCount:
		return admissionRejected
	}
	return admissionRejected
}

const admissionErrorBytes = 512

func installationStage(result offload.NativePairResult, requested uint32, err error) admissionStage {
	if err != nil {
		return admissionInstallationFailed
	}
	if result.State != offload.NativePairInstalled || result.InstalledDirections != requested {
		return admissionInstallationIncomplete
	}
	return admissionInstalled
}

type admissionError struct {
	text      [admissionErrorBytes]byte
	length    int
	truncated bool
	present   bool
}

func freezeAdmissionError(err error) admissionError {
	var detail admissionError
	if err == nil {
		return detail
	}
	message := err.Error()
	detail.present = true
	detail.length = copy(detail.text[:], message)
	detail.truncated = len(message) > len(detail.text)
	return detail
}

func (detail admissionError) LogValue() slog.Value {
	return slog.GroupValue(slog.Bool("present", detail.present), slog.String("message", string(detail.text[:detail.length])), slog.Bool("truncated", detail.truncated))
}

// Copy scalar conntrack evidence because ProtoInfo contains mutable pointers.
type admissionConnection struct {
	Valid      bool
	ID         uint32
	Mark       uint32
	Timeout    uint32
	Status     conntrack.Status
	Zone       uint16
	TCPPresent bool
	TCPState   uint8
	Original   conntrack.Tuple
	Reply      conntrack.Tuple
}

func freezeAdmissionConnection(flow conntrack.Flow) admissionConnection {
	result := admissionConnection{Valid: true, ID: flow.ID, Mark: flow.Mark, Timeout: flow.Timeout, Status: flow.Status, Zone: flow.Zone, TCPPresent: false, TCPState: 0, Original: flow.TupleOrig, Reply: flow.TupleReply}
	if flow.ProtoInfo.TCP != nil {
		result.TCPPresent, result.TCPState = true, flow.ProtoInfo.TCP.State
	}
	return result
}

type admissionRoute struct {
	Queried        bool
	Valid          bool
	Usable         bool
	Selector       netif.PacketSelector
	OutputIndex    int
	Table          int
	Type           int
	Flags          int
	MTU            int
	Gateway        netip.Addr
	NeighborState  int
	SourceMAC      [6]byte
	DestinationMAC [6]byte
}

func freezeAdmissionRoute(selector netif.PacketSelector, path netif.ForwardingPath, usable bool) admissionRoute {
	var result admissionRoute
	result.Queried, result.Usable, result.Selector = true, usable, selector
	if path.Link == nil {
		return result
	}
	result.Valid, result.OutputIndex = true, path.Link.Attrs().Index
	result.Table, result.Type, result.Flags, result.MTU = path.Route.Table, path.Route.Type, path.Route.Flags, path.Route.MTU
	result.Gateway, _ = netip.AddrFromSlice(path.Route.Gw)
	result.NeighborState = path.Neighbor.State
	copy(result.SourceMAC[:], path.Link.Attrs().HardwareAddr)
	copy(result.DestinationMAC[:], path.Neighbor.HardwareAddr)
	return result
}

type admissionSample struct {
	observation         offload.NativeObservation
	observedAt          time.Time
	age                 time.Duration
	ageChecked          bool
	maxAge              time.Duration
	ifindex             int
	inputName           string
	inputBound          bool
	hasOrigin           bool
	inputMAC            [6]byte
	connection          admissionConnection
	currentConnection   admissionConnection
	route               admissionRoute
	outputMTU           int
	mtuChecked          bool
	kernelTuple         conntrack.Tuple
	kernelTupleValid    bool
	wan                 string
	generationChecked   bool
	nptBefore           uint64
	nptAfter            uint64
	routingBefore       uint64
	routingAfter        uint64
	policyReady         bool
	policyChecked       bool
	eligibility         Eligibility
	eligibilityValid    bool
	retirement          offload.NativeRetirementStats
	retirementValid     bool
	requireShared       bool
	tracked             int
	capacity            uint32
	installation        offload.NativePairResult
	installationValid   bool
	requestedDirections uint32
	runtimePresent      bool
	policyBlocked       bool
	paused              bool
	failure             admissionError
	decodeFailure       admissionError
}

// The module lock protects these fixed slots and their monotonic counts.
type admissionDiagnostics struct {
	counts  [admissionStageCount]uint64
	present [admissionStageCount]bool
	samples [admissionStageCount]admissionSample
}

func (module *Module) recordAdmission(stage admissionStage, observation packetObservation, details *admissionSample, err error) {
	if !module.cfg.Settings.ObservationDiagnostics {
		return
	}
	if module.admissionDiagnostics == nil {
		module.admissionDiagnostics = new(admissionDiagnostics)
	}
	diagnostics := module.admissionDiagnostics
	diagnostics.counts[stage]++
	if diagnostics.present[stage] {
		return
	}
	sample := &diagnostics.samples[stage]
	if details != nil {
		*sample = *details
	}
	sample.observation, sample.observedAt = observation.NativeObservation, observation.at
	sample.decodeFailure = freezeAdmissionError(sample.observation.DecodeError)
	sample.observation.DecodeError = nil
	if !sample.ageChecked {
		sample.age = (clock.Real{}).Now().Sub(observation.at)
	}
	sample.maxAge = module.cfg.Settings.ObservationMaxAge
	sample.ifindex = observation.ifindex
	input, bound := module.links[observation.ifindex]
	sample.inputBound, sample.hasOrigin = bound, input.hasOrigin
	sample.inputName = input.kernel.Name
	copy(sample.inputMAC[:], input.kernel.HardwareAddr)
	sample.runtimePresent, sample.policyBlocked, sample.paused = module.runtime != nil, module.policyBlocked, module.admissionPaused.Load()
	sample.failure = freezeAdmissionError(err)
	diagnostics.present[stage] = true
}

func (sample admissionSample) logValue() slog.Value {
	attributes := []slog.Attr{
		slog.Time("observed_at", sample.observedAt), slog.Duration("age", sample.age), slog.Duration("max_age", sample.maxAge),
		slog.Any("observation", sample.observation), slog.Any("decode_error", sample.decodeFailure),
		slog.Bool("input_bound", sample.inputBound), slog.Bool("has_origin", sample.hasOrigin), slog.Any("input_mac", sample.inputMAC),
		slog.Any("connection", sample.connection), slog.Any("current_connection", sample.currentConnection),
		slog.Bool("runtime_present", sample.runtimePresent), slog.Bool("policy_blocked", sample.policyBlocked), slog.Bool("paused", sample.paused),
		slog.Any("error", sample.failure),
	}
	if sample.kernelTupleValid {
		attributes = append(attributes, slog.Any("kernel_tuple", sample.kernelTuple))
	}
	if sample.eligibilityValid {
		attributes = append(attributes, slog.String("wan", sample.wan), slog.Uint64("eligibility", uint64(sample.eligibility)))
	}
	if sample.route.Queried {
		attributes = append(attributes, slog.Any("route", sample.route))
	}
	if sample.mtuChecked {
		attributes = append(attributes, slog.Int("output_mtu", sample.outputMTU))
	}
	if sample.generationChecked {
		attributes = append(attributes, slog.Uint64("npt_before", sample.nptBefore), slog.Uint64("npt_after", sample.nptAfter), slog.Uint64("routing_before", sample.routingBefore), slog.Uint64("routing_after", sample.routingAfter), slog.Bool("policy_checked", sample.policyChecked), slog.Bool("policy_ready", sample.policyReady))
	}
	if sample.retirementValid {
		attributes = append(attributes, slog.Any("retirement", sample.retirement), slog.Bool("require_shared", sample.requireShared), slog.Int("tracked", sample.tracked), slog.Uint64("capacity", uint64(sample.capacity)))
	}
	if sample.installationValid {
		attributes = append(attributes, slog.Any("installation", sample.installation), slog.Uint64("requested_directions", uint64(sample.requestedDirections)))
	}
	return slog.GroupValue(attributes...)
}

func (module *Module) logAdmissionDiagnostics(ctx context.Context) {
	module.Lock()
	if module.admissionDiagnostics == nil {
		module.Unlock()
		return
	}
	snapshot := *module.admissionDiagnostics
	module.admissionDiagnostics.present = [admissionStageCount]bool{}
	module.admissionDiagnostics.samples = [admissionStageCount]admissionSample{}
	module.Unlock()
	attributes := make([]slog.Attr, 0, admissionStageCount)
	for stage, count := range snapshot.counts {
		attributes = append(attributes, slog.Uint64(admissionStage(stage).String(), count))
	}
	samples := make([]slog.Attr, 0, admissionStageCount)
	for stage, present := range snapshot.present {
		if present {
			sample := snapshot.samples[stage]
			value := slog.GroupValue(slog.String("outcome", string(admissionStage(stage).outcome())), slog.String("interface", sample.inputName), slog.Int("ifindex", sample.ifindex), slog.Attr{Key: "sample", Value: sample.logValue()})
			samples = append(samples, slog.Attr{Key: admissionStage(stage).String(), Value: value})
		}
	}
	module.Log.InfoContext(ctx, "native admission diagnostics", "counters", slog.GroupValue(attributes...), "samples", slog.GroupValue(samples...))
}
