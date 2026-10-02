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
	if plan == nil || spec.DistributionSamples <= 0 || plan.MinimumSamplesPerProvider <= 0 || len(plan.Providers) == 0 || len(plan.Requests) == 0 {
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
		mac, err := net.ParseMAC(provider.DestinationMAC)
		if provider.ConnectionID == "" || identities[provider.ConnectionID] || provider.Family != spec.Family || provider.Bridge == "" || provider.PortInterface == "" || provider.Weight <= 0 || err != nil || mac.String() != provider.DestinationMAC {
			return errors.New("distribution provider requires unique identity, family, kernel mapping, canonical MAC and positive weight")
		}
		identities[provider.ConnectionID] = true
	}
	for _, request := range plan.Requests {
		if request.Operation != OperationHTTP || request.Family != spec.Family || request.DistributionPlan != nil {
			return errors.New("distribution probes must be HTTP checks in the required family")
		}
		if err := Validate(request); err != nil {
			return err
		}
	}
	return nil
}

func (executor *Executor) distribution(ctx context.Context, spec CheckSpec, result Result) Result {
	plan := spec.DistributionPlan
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
	if eligible == 0 || spec.DistributionSamples < eligible*plan.MinimumSamplesPerProvider {
		result.Availability, result.Reason = AvailabilityMissing, "distribution has no current eligible providers or too few configured samples"
		return result
	}
	notify := make(chan struct{}, len(providers))
	var captures []*ingressCapture
	defer func() {
		for _, capture := range captures {
			_ = capture.finish()
		}
	}()
	for _, provider := range providers {
		capture, err := executor.openIngressCapture(ctx, provider, spec.DistributionSamples*16, notify)
		if err != nil {
			result.Reason = err.Error()
			return result
		}
		captures = append(captures, capture)
		result.DistributionCaptures = append(result.DistributionCaptures, capture.ready)
	}
	if err := executor.distributionRequests(ctx, spec, captures, notify, &result); err != nil {
		result.Availability, result.Reason = AvailabilityMissing, err.Error()
		return result
	}
	var captureError error
	for _, capture := range captures {
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
		frame, matched := capturedIngress(captures, sample.StartedAt, sample.Path)
		if !matched {
			result.Availability, result.Reason = AvailabilityMissing, "completed capture contains ambiguous or unmatched provider ingress"
			return result
		}
		sample.Ingress, sample.ConnectionID = frame, frame.ConnectionID
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
		result.Reason = "observed distribution omits an eligible provider or selects an ineligible provider"
		return result
	}
	result.Outcome, result.Reason = OutcomePass, "actual ingress samples satisfy configured provider coverage"
	return result
}

func (executor *Executor) distributionRequests(ctx context.Context, spec CheckSpec, captures []*ingressCapture, notify <-chan struct{}, result *Result) error {
	for index := range spec.DistributionSamples {
		request := spec.DistributionPlan.Requests[index%len(spec.DistributionPlan.Requests)]
		started := executor.config.Clock.Now().UTC()
		reply := executor.Run(ctx, request)
		var sample DistributionSample
		sample.StartedAt, sample.ResponseBody = started, reply.ResponseBody
		sample.At, sample.CheckID, sample.Observer, sample.Path = reply.ObservedAt, reply.CheckID, reply.Observer, reply.Path
		sample.Availability, sample.Outcome, sample.HTTPStatus = reply.Availability, reply.Outcome, reply.HTTPStatus
		result.Distribution = append(result.Distribution, sample)
		if !RequiredPassed(request, reply, executor.config.Clock.Now()) {
			return errors.New("distribution request lacks an expected application reply or required path")
		}
		frame, matched := awaitIngress(ctx, captures, notify, started, reply.Path)
		if !matched {
			return errors.New("distribution request has ambiguous, rewritten or unobserved ingress tuple")
		}
		sample.Ingress, sample.ConnectionID = frame, frame.ConnectionID
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

func awaitIngress(ctx context.Context, captures []*ingressCapture, notify <-chan struct{}, started time.Time, path Path) (TCPIngress, bool) {
	var absent TCPIngress
	for {
		found, matched := capturedIngress(captures, started, path)
		if matched {
			return found, true
		}
		select {
		case <-ctx.Done():
			return absent, false
		case <-notify:
		}
	}
}

func capturedIngress(captures []*ingressCapture, started time.Time, path Path) (TCPIngress, bool) {
	var found TCPIngress
	matched := false
	for _, capture := range captures {
		capture.mu.Lock()
		for _, frame := range capture.frames {
			if frame.At.Before(started) || frame.SourcePort != path.SourcePort || frame.Destination != path.Destination || frame.DestinationPort != path.DestinationPort {
				continue
			}
			if matched && found.ConnectionID != frame.ConnectionID {
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
	keys := make(map[string]bool)
	for _, sample := range samples {
		if mode == "random" {
			return true
		}
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
			if share.Samples < plan.MinimumSamplesPerProvider {
				return false
			}
		} else if share.Samples != 0 {
			return false
		}
	}
	return true
}

func requiredDistributionPassed(spec CheckSpec, result Result, now time.Time) bool {
	if validateDistribution(spec) != nil || len(result.Distribution) != spec.DistributionSamples {
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
	for index, sample := range result.Distribution {
		request := plan.Requests[index%len(plan.Requests)]
		if !requiredDistributionSample(request, sample, now) {
			return false
		}
		matched := false
		for providerIndex, provider := range plan.Providers {
			if provider.ConnectionID == sample.ConnectionID && provider.DestinationMAC == sample.Ingress.DestinationMAC && !result.DistributionCaptures[providerIndex].At.After(sample.StartedAt) {
				matched = true
			}
		}
		if !matched {
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

func requiredProviderReady(provider ProviderIngress, share ProviderShare, ready CaptureReady, now time.Time, ageSeconds int) bool {
	return share.Provider == provider && observationCurrent(provider.ObservedAt, now, ageSeconds) && ready.ConnectionID == provider.ConnectionID && ready.Interface == provider.Bridge && ready.PortInterface == provider.PortInterface && ready.DestinationMAC == provider.DestinationMAC && observationCurrent(ready.At, now, ageSeconds)
}

func requiredDistributionSample(request CheckSpec, sample DistributionSample, now time.Time) bool {
	var reply Result
	reply.CheckID, reply.Dimension, reply.Operation, reply.Target, reply.Family = sample.CheckID, request.Dimension, OperationHTTP, request.Target, request.Family
	reply.Observer, reply.ObservedAt, reply.Availability, reply.Outcome = sample.Observer, sample.At, sample.Availability, sample.Outcome
	reply.Path, reply.HTTPStatus, reply.ResponseBody = sample.Path, sample.HTTPStatus, sample.ResponseBody
	return RequiredPassed(request, reply, now) && !sample.StartedAt.IsZero() && !sample.StartedAt.After(sample.At) && sample.Ingress.Source.IsValid() && sample.Ingress.Source.Is4() == (request.Family == FamilyIPv4) && !sample.Ingress.At.Before(sample.StartedAt) && sample.Ingress.SourcePort != 0 && sample.Ingress.SourcePort == sample.Path.SourcePort && sample.Ingress.Destination == sample.Path.Destination && sample.Ingress.DestinationPort == sample.Path.DestinationPort && sample.Ingress.ConnectionID == sample.ConnectionID
}
