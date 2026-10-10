package wanroutes

import (
	"maps"

	"goodkind.io/mwan/internal/wanstate"
)

type familyProbeReasons struct {
	v4 string
	v6 string
}

func familyProbeReason(result wanstate.ProbeResult, verdict wanstate.Health) string {
	if result != wanstate.ProbePass && result != wanstate.ProbeFail {
		return ""
	}
	switch verdict {
	case wanstate.HealthHealthy:
		return ""
	case wanstate.HealthUnhealthy:
		return reasonProbeFailed
	case wanstate.HealthUnknown:
		return reasonProbePending
	}
	return reasonProbePending
}

func (m *Module) familyHealth() map[string]wanstate.MemberHealth {
	if m.Env == nil || m.Env.LiveState == nil {
		return nil
	}
	return m.Env.LiveState.Snapshot().Health
}

func probeExclusions(wans []WAN, health map[string]wanstate.MemberHealth) map[string]familyProbeReasons {
	exclusions := make(map[string]familyProbeReasons, len(wans))
	for _, wan := range wans {
		member := health[wan.Key()]
		exclusions[wan.Key()] = familyProbeReasons{
			v4: familyProbeReason(member.V4, member.V4Verdict),
			v6: familyProbeReason(member.V6, member.V6Verdict),
		}
	}
	return exclusions
}

func withoutExcludedFamilies(current gateways, exclusions map[string]familyProbeReasons) gateways {
	remaining := maps.Clone(current)
	for key, reasons := range exclusions {
		wanGateways, found := remaining[key]
		if !found {
			continue
		}
		if reasons.v4 != "" {
			wanGateways.V4 = ""
		}
		if reasons.v6 != "" {
			wanGateways = wanGateways.withoutV6()
		}
		remaining[key] = wanGateways
	}
	return remaining
}

func (m *Module) probeReasonV6(key string) string {
	if reason := m.tunnelReasons[key]; reason != "" {
		return reason
	}
	return m.probeReasons[key].v6
}
