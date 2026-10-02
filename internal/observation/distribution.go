package observation

import (
	"context"
	"errors"
	"net"
	"slices"
	"time"

	"goodkind.io/mwan/internal/wanstate"
)

func validateDistribution(spec CheckSpec) error {
	plan := spec.DistributionPlan
	if plan == nil || spec.DistributionSamples <= 0 || len(plan.Providers) == 0 || len(plan.Requests) == 0 || len(plan.Transit) == 0 {
		return errors.New("distribution requires providers, probes and positive sample requirements")
	}
	if spec.Observer.Kind != EndpointLocal {
		return errors.New("distribution coordinator must use local hypervisor observation")
	}
	if plan.HashMode != "random" && plan.HashMode != "source" && plan.HashMode != "source-destination" {
		return errors.New("unsupported configured distribution hash mode")
	}
	identities := make(map[string]bool, len(plan.Providers))
	for _, provider := range plan.Providers {
		if !captureMappingValid(provider, spec.Family) || identities[provider.ConnectionID] || provider.Weight <= 0 {
			return errors.New("distribution provider requires unique identity, family, kernel mapping, canonical MAC and positive weight")
		}
		identities[provider.ConnectionID] = true
	}
	for _, request := range plan.Requests {
		if err := validateDistributionRequest(request, spec.Family); err != nil {
			return err
		}
	}
	for _, transit := range plan.Transit {
		if !captureMappingValid(transit, spec.Family) {
			return errors.New("distribution transit requires verified bridge, gateway port and MAC")
		}
	}
	return nil
}

func captureMappingValid(mapping ProviderIngress, family Family) bool {
	mac, err := net.ParseMAC(mapping.DestinationMAC)
	return mapping.ConnectionID != "" && mapping.Family == family && mapping.Bridge != "" && mapping.PortInterface != "" && err == nil && mac.String() == mapping.DestinationMAC
}

func validateDistributionRequest(request CheckSpec, family Family) error {
	if request.Operation != OperationHTTP || request.Dimension != DimensionDownstreamApplication || request.Family != family || request.DistributionPlan != nil || request.Router != RouterPrimary || !request.ExpectedNextHop.IsValid() || !request.Source.IsValid() || request.ConnectionID != "" {
		return errors.New("distribution requires downstream HTTP over an explicit primary gateway without provider binding")
	}
	return Validate(request)
}

func (executor *Executor) distribution(ctx context.Context, spec CheckSpec, result Result) Result {
	plan := spec.DistributionPlan
	if !distributionCalibrated(spec) {
		result.Availability, result.Reason = AvailabilityMissing, "distribution lacks calibration for the exact sample count, hash policy and eligible provider tiers and weights"
		return result
	}
	providers, current := executor.distributionProviders(spec)
	if !current {
		result.Availability, result.Reason = AvailabilityStale, "distribution provider policy is absent, stale or inconsistent with current state"
		return result
	}
	eligible := 0
	for _, provider := range providers {
		if provider.Eligible && provider.Tier == plan.ActiveTier {
			eligible++
		}
		result.DistributionProviders = append(result.DistributionProviders, ProviderShare{Provider: provider, Samples: 0})
	}
	if eligible == 0 {
		result.Availability, result.Reason = AvailabilityMissing, "distribution has no current eligible providers"
		return result
	}
	notify := make(chan struct{}, len(providers)+len(plan.Transit))
	var captures []*ingressCapture
	var transitCaptures []*ingressCapture
	defer func() {
		for _, capture := range append(captures, transitCaptures...) {
			_ = capture.finish()
		}
	}()
	var captureError error
	captures, result.DistributionCaptures, captureError = executor.openDistributionCaptures(ctx, spec, providers, notify)
	if captureError != nil {
		result.Reason = captureError.Error()
		return result
	}
	transitCaptures, result.TransitCaptures, captureError = executor.openDistributionCaptures(ctx, spec, plan.Transit, notify)
	if captureError != nil {
		result.Reason = captureError.Error()
		return result
	}
	if err := executor.distributionRequests(ctx, spec, captures, transitCaptures, notify, &result); err != nil {
		result.Availability, result.Reason = AvailabilityMissing, err.Error()
		return result
	}
	for _, capture := range append(captures, transitCaptures...) {
		if err := capture.finish(); err != nil {
			captureError = err
		}
	}
	if captureError != nil {
		result.Reason = captureError.Error()
		return result
	}
	for index := range result.Distribution {
		sample := &result.Distribution[index]
		frame, transit, matched := capturedIngress(captures, transitCaptures, sample.StartedAt, sample.At, sample.Path)
		if !matched {
			result.Availability, result.Reason = AvailabilityMissing, "completed capture contains ambiguous or unmatched provider ingress"
			return result
		}
		sample.Ingress, sample.Transit, sample.ConnectionID = frame, transit, frame.ConnectionID
		for shareIndex := range result.DistributionProviders {
			if result.DistributionProviders[shareIndex].Provider.ConnectionID == frame.ConnectionID {
				result.DistributionProviders[shareIndex].Samples++
			}
		}
	}
	if !distributionDiverse(plan.HashMode, result.Distribution, eligible) {
		result.Availability, result.Reason = AvailabilityMissing, "distribution samples lack the source or destination diversity required by the configured hash mode"
		return result
	}
	result.Availability, result.Outcome = AvailabilityComplete, OutcomeFail
	if !distributionSharesPassed(plan, result.DistributionProviders) {
		result.Reason = "observed distribution violates calibrated provider shares or selects an ineligible provider"
		return result
	}
	result.Outcome, result.Reason = OutcomePass, "actual ingress samples satisfy calibrated provider shares"
	return result
}

func (executor *Executor) openDistributionCaptures(ctx context.Context, spec CheckSpec, mappings []ProviderIngress, notify chan<- struct{}) ([]*ingressCapture, []CaptureReady, error) {
	var captures []*ingressCapture
	var readiness []CaptureReady
	for _, mapping := range mappings {
		if !observationCurrent(mapping.ObservedAt, executor.config.Clock.Now(), spec.MaxAgeSeconds) {
			return captures, readiness, errors.New("distribution capture mapping is stale")
		}
		capture, err := executor.openIngressCapture(ctx, mapping, spec.DistributionSamples*16, notify)
		if err != nil {
			return captures, readiness, err
		}
		captures = append(captures, capture)
		readiness = append(readiness, capture.ready)
	}
	return captures, readiness, nil
}

func (executor *Executor) distributionRequests(ctx context.Context, spec CheckSpec, captures, transitCaptures []*ingressCapture, notify <-chan struct{}, result *Result) error {
	for index := range spec.DistributionSamples {
		request := spec.DistributionPlan.Requests[index%len(spec.DistributionPlan.Requests)]
		started := executor.config.Clock.Now().UTC()
		reply := executor.Run(ctx, request)
		var sample DistributionSample
		sample.StartedAt, sample.ResponseBody = started, reply.ResponseBody
		sample.At, sample.CheckID, sample.Observer, sample.Path = reply.ObservedAt, reply.CheckID, reply.Observer, reply.Path
		sample.Availability, sample.Outcome, sample.HTTPStatus = reply.Availability, reply.Outcome, reply.HTTPStatus
		sample.Reason = reply.Reason
		result.Distribution = append(result.Distribution, sample)
		if !RequiredPassed(request, reply, executor.config.Clock.Now()) {
			return errors.New("distribution request lacks an expected application reply or required path")
		}
		frame, transit, matched := awaitIngress(ctx, captures, transitCaptures, notify, started, reply.ObservedAt, reply.Path)
		if !matched {
			return errors.New("distribution request has ambiguous, rewritten or unobserved ingress tuple")
		}
		sample.Ingress, sample.Transit, sample.ConnectionID = frame, transit, frame.ConnectionID
		result.Distribution[len(result.Distribution)-1] = sample
	}
	return nil
}

func (executor *Executor) distributionProviders(spec CheckSpec) ([]ProviderIngress, bool) {
	plan := spec.DistributionPlan
	providers := slices.Clone(plan.Providers)
	now := executor.config.Clock.Now()
	if !observationCurrent(plan.ObservedAt, now, spec.MaxAgeSeconds) {
		return providers, false
	}
	var state wanstate.Snapshot
	if executor.config.State != nil {
		state = executor.config.State.Snapshot()
		if !state.TierValid || state.ActiveTier != plan.ActiveTier || state.RoutingGeneration != plan.RoutingGeneration {
			return providers, false
		}
	}
	for index := range providers {
		provider := &providers[index]
		if !observationCurrent(provider.ObservedAt, now, spec.MaxAgeSeconds) {
			return providers, false
		}
		if executor.config.State != nil {
			connection, found := state.Connections[provider.ConnectionID]
			routing, routed := state.Routing[provider.ConnectionID]
			if !found || !routed || !observationCurrent(connection.ObservedAt, now, spec.MaxAgeSeconds) {
				return providers, false
			}
			ready := routing.V4Ready
			if spec.Family == FamilyIPv6 {
				ready = routing.V6Ready
			}
			eligible := routing.Carrying && ready && state.Health[provider.ConnectionID].Verdict == wanstate.HealthHealthy
			if provider.Eligible != eligible {
				return providers, false
			}
		}
	}
	return providers, true
}

func observationCurrent(observed, now time.Time, ageSeconds int) bool {
	age := now.Sub(observed)
	return !observed.IsZero() && age >= 0 && age <= time.Duration(ageSeconds)*time.Second
}

func awaitIngress(ctx context.Context, captures, transitCaptures []*ingressCapture, notify <-chan struct{}, started, ended time.Time, path Path) (TCPIngress, TCPIngress, bool) {
	var absent TCPIngress
	for {
		found, transit, matched := capturedIngress(captures, transitCaptures, started, ended, path)
		if matched {
			return found, transit, true
		}
		select {
		case <-ctx.Done():
			return absent, absent, false
		case <-notify:
		}
	}
}

func capturedIngress(captures, transitCaptures []*ingressCapture, started, ended time.Time, path Path) (TCPIngress, TCPIngress, bool) {
	transit, matched := capturedTransit(transitCaptures, started, ended, path)
	if !matched {
		var absent TCPIngress
		return absent, absent, false
	}
	var found TCPIngress
	matched = false
	for _, capture := range captures {
		capture.mu.Lock()
		for _, frame := range capture.frames {
			if frame.At.Before(started) || frame.At.After(ended) || frame.Sequence != transit.Sequence || frame.Destination != transit.Destination || frame.DestinationPort != transit.DestinationPort {
				continue
			}
			if matched && found.ConnectionID != frame.ConnectionID {
				capture.mu.Unlock()
				var absent TCPIngress
				return absent, transit, false
			}
			found, matched = frame, true
		}
		capture.mu.Unlock()
	}
	return found, transit, matched
}

func capturedTransit(captures []*ingressCapture, started, ended time.Time, path Path) (TCPIngress, bool) {
	var found TCPIngress
	matched := false
	for _, capture := range captures {
		capture.mu.Lock()
		for _, frame := range capture.frames {
			if frame.At.Before(started) || frame.At.After(ended) || frame.Source != path.Source || frame.SourcePort != path.SourcePort || frame.Destination != path.Destination || frame.DestinationPort != path.DestinationPort {
				continue
			}
			if matched && frame.Sequence != found.Sequence {
				capture.mu.Unlock()
				var absent TCPIngress
				return absent, false
			}
			found, matched = frame, true
		}
		capture.mu.Unlock()
	}
	return found, matched
}

func distributionDiverse(mode string, samples []DistributionSample, providers int) bool {
	if mode == "random" {
		return len(samples) > 0
	}
	keys := make(map[string]bool)
	for _, sample := range samples {
		if !sample.Path.Source.IsValid() {
			return false
		}
		key := sample.Path.Source.String()
		if mode == "source-destination" {
			key += "/" + sample.Path.Destination.String()
		}
		keys[key] = true
	}
	return len(keys) >= providers
}

func distributionSharesPassed(plan *DistributionPlan, shares []ProviderShare) bool {
	for _, share := range shares {
		if share.Provider.Eligible && share.Provider.Tier == plan.ActiveTier {
			bound, exists := calibratedProvider(plan, share.Provider.ConnectionID)
			if !exists || share.Samples < bound.MinSamples || share.Samples > bound.MaxSamples {
				return false
			}
		} else if share.Samples != 0 {
			return false
		}
	}
	return true
}

func calibratedProvider(plan *DistributionPlan, id string) (CalibratedProvider, bool) {
	if plan.Calibration != nil {
		for _, provider := range plan.Calibration.Providers {
			if provider.ConnectionID == id {
				return provider, true
			}
		}
	}
	var absent CalibratedProvider
	return absent, false
}

func distributionCalibrated(spec CheckSpec) bool {
	plan := spec.DistributionPlan
	calibration := plan.Calibration
	if calibration == nil || calibration.HashMode != plan.HashMode || calibration.ActiveTier != plan.ActiveTier || calibration.Samples != spec.DistributionSamples {
		return false
	}
	seen := make(map[string]bool)
	minimum, maximum := 0, 0
	for _, calibrated := range calibration.Providers {
		if seen[calibrated.ConnectionID] || calibrated.MinSamples < 0 || calibrated.MaxSamples < calibrated.MinSamples || calibrated.MaxSamples > calibration.Samples {
			return false
		}
		seen[calibrated.ConnectionID] = true
		minimum += calibrated.MinSamples
		maximum += calibrated.MaxSamples
	}
	eligible := 0
	for _, provider := range plan.Providers {
		if !provider.Eligible || provider.Tier != plan.ActiveTier {
			continue
		}
		eligible++
		calibrated, exists := calibratedProvider(plan, provider.ConnectionID)
		if !exists || calibrated.Tier != provider.Tier || calibrated.Weight != provider.Weight {
			return false
		}
	}
	return eligible > 0 && eligible == len(calibration.Providers) && minimum <= calibration.Samples && maximum >= calibration.Samples
}

func requiredDistributionPassed(spec CheckSpec, result Result, now time.Time) bool {
	if validateDistribution(spec) != nil || len(result.Distribution) != spec.DistributionSamples {
		return false
	}
	if !distributionCalibrated(spec) || len(result.TransitCaptures) != len(spec.DistributionPlan.Transit) {
		return false
	}
	plan := spec.DistributionPlan
	if !observationCurrent(plan.ObservedAt, now, spec.MaxAgeSeconds) || len(result.DistributionProviders) != len(plan.Providers) || len(result.DistributionCaptures) != len(plan.Providers) {
		return false
	}
	counts := make(map[string]int, len(plan.Providers))
	eligible := 0
	for index, provider := range plan.Providers {
		share := result.DistributionProviders[index]
		ready := result.DistributionCaptures[index]
		if !requiredProviderReady(provider, share, ready, now, spec.MaxAgeSeconds) {
			return false
		}
		if provider.Eligible && provider.Tier == plan.ActiveTier {
			eligible++
		}
	}
	for index, transit := range plan.Transit {
		ready := result.TransitCaptures[index]
		if !requiredProviderReady(transit, ProviderShare{Provider: transit, Samples: 0}, ready, now, spec.MaxAgeSeconds) {
			return false
		}
	}
	for index, sample := range result.Distribution {
		request := plan.Requests[index%len(plan.Requests)]
		if !requiredDistributionSample(request, sample, now) {
			return false
		}
		if !frameCaptureIdentified(plan.Providers, result.DistributionCaptures, sample.Ingress, sample.StartedAt) || !frameCaptureIdentified(plan.Transit, result.TransitCaptures, sample.Transit, sample.StartedAt) {
			return false
		}
		counts[sample.ConnectionID]++
	}
	for _, share := range result.DistributionProviders {
		if share.Samples != counts[share.Provider.ConnectionID] {
			return false
		}
	}
	return eligible > 0 && distributionDiverse(plan.HashMode, result.Distribution, eligible) && distributionSharesPassed(plan, result.DistributionProviders)
}

func frameCaptureIdentified(mappings []ProviderIngress, captures []CaptureReady, frame TCPIngress, started time.Time) bool {
	for index, mapping := range mappings {
		if mapping.ConnectionID == frame.ConnectionID && mapping.DestinationMAC == frame.DestinationMAC && !captures[index].At.After(started) {
			return true
		}
	}
	return false
}

func requiredProviderReady(provider ProviderIngress, share ProviderShare, ready CaptureReady, now time.Time, ageSeconds int) bool {
	return share.Provider == provider && observationCurrent(provider.ObservedAt, now, ageSeconds) && ready.ConnectionID == provider.ConnectionID && ready.Interface == provider.Bridge && ready.PortInterface == provider.PortInterface && ready.DestinationMAC == provider.DestinationMAC && observationCurrent(ready.At, now, ageSeconds)
}

func requiredDistributionSample(request CheckSpec, sample DistributionSample, now time.Time) bool {
	var reply Result
	reply.CheckID, reply.Dimension, reply.Operation, reply.Target, reply.Family = sample.CheckID, request.Dimension, OperationHTTP, request.Target, request.Family
	reply.Observer, reply.ObservedAt, reply.Availability, reply.Outcome = sample.Observer, sample.At, sample.Availability, sample.Outcome
	reply.Path, reply.HTTPStatus, reply.ResponseBody = sample.Path, sample.HTTPStatus, sample.ResponseBody
	return RequiredPassed(request, reply, now) && !sample.StartedAt.IsZero() && !sample.StartedAt.After(sample.At) && distributionSampleCaptured(request, sample)
}

func distributionSampleCaptured(request CheckSpec, sample DistributionSample) bool {
	return sample.Ingress.Source.IsValid() && sample.Ingress.Source.Is4() == (request.Family == FamilyIPv4) && !sample.Ingress.At.Before(sample.StartedAt) && !sample.Ingress.At.After(sample.At) && sample.Ingress.SourcePort != 0 && sample.Transit.Source == sample.Path.Source && sample.Transit.SourcePort == sample.Path.SourcePort && sample.Transit.Sequence == sample.Ingress.Sequence && !sample.Transit.At.Before(sample.StartedAt) && !sample.Transit.At.After(sample.At) && sample.Ingress.Destination == sample.Path.Destination && sample.Transit.Destination == sample.Path.Destination && sample.Transit.DestinationPort == sample.Path.DestinationPort && sample.Ingress.DestinationPort == sample.Path.DestinationPort && sample.Ingress.ConnectionID == sample.ConnectionID
}
