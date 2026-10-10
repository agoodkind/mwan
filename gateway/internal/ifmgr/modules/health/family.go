package health

type familyStatuses struct {
	v4 wanStatus
	v6 wanStatus
}

func advanceFamily(wan WAN, cfg Config, current wanStatus, probed, passed bool) (wanStatus, bool) {
	if !probed {
		return current, false
	}
	if !current.State.Valid() {
		current.State = StateUnknown
	}
	return advanceHealth(current, passed, wan.failureThreshold(cfg), wan.recoveryThreshold(cfg))
}

func (m *Module) advanceFamilies(wan WAN, result probeResult) bool {
	if m.families == nil {
		m.families = make(map[string]familyStatuses, len(m.cfg.WANs))
	}
	current := m.families[wan.Key()]
	v4, v4Changed := advanceFamily(wan, m.cfg, current.v4, result.V4Probed, result.V4Passed)
	v6, v6Changed := advanceFamily(wan, m.cfg, current.v6, result.V6Probed, result.V6Passed)
	m.families[wan.Key()] = familyStatuses{v4: v4, v6: v6}
	return v4Changed || v6Changed
}
