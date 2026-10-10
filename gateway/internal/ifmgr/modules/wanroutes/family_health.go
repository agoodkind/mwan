package wanroutes

import "goodkind.io/mwan/internal/wanstate"

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

// Callers must lock the module.
func (m *Module) excludeUnhealthyFamilies(current gateways) {
	m.probeReasons = make(map[string]familyProbeReasons, len(m.cfg.WANs))
	if m.Env == nil || m.Env.LiveState == nil {
		return
	}
	health := m.Env.LiveState.Snapshot().Health
	for _, wan := range m.cfg.WANs {
		member := health[wan.Key()]
		reasons := familyProbeReasons{
			v4: familyProbeReason(member.V4, member.V4Verdict),
			v6: familyProbeReason(member.V6, member.V6Verdict),
		}
		wanGateways := current[wan.Key()]
		if reasons.v4 != "" {
			wanGateways.V4 = ""
		}
		if reasons.v6 != "" {
			wanGateways = wanGateways.withoutV6()
		}
		current[wan.Key()] = wanGateways
		m.probeReasons[wan.Key()] = reasons
	}
}

func (m *Module) probeReasonV6(key string) string {
	if reason := m.tunnelReasons[key]; reason != "" {
		return reason
	}
	return m.probeReasons[key].v6
}
