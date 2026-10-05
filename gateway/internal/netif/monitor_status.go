package netif

func (m *Monitor) observationEvent(kind EventKind, reason string) Event {
	var event Event
	event.Kind = kind
	event.Iface = m.cfg.Iface
	event.ConnectionID = m.connectionID()
	event.ObservedAt = realClock{}.Now()
	event.Reason = reason
	return event
}

func (m *Monitor) connectionID() string {
	if m.cfg.Connection != nil {
		return m.cfg.Connection.ID.String()
	}
	return m.cfg.ConnectionID
}

func (m *Monitor) statusCancelledLocked() bool {
	if m.stopped {
		return true
	}
	select {
	case <-m.cancelled:
		return true
	default:
		return false
	}
}

func (m *Monitor) markStaleLocked(reason string) {
	if m.statusCancelledLocked() {
		return
	}
	if !m.stale {
		m.stale = true
		m.failed = false
		for draining := true; draining; {
			select {
			case <-m.Events:
			default:
				draining = false
			}
		}
		select {
		case m.Events <- m.observationEvent(EvObservationStale, reason):
		default:
			m.log.Warn("monitor: failed to queue stale observation", "reason", reason)
		}
	}
	select {
	case m.resync <- struct{}{}:
	default:
	}
}

func (m *Monitor) markFailedLocked(reason string) {
	if m.statusCancelledLocked() {
		return
	}
	if m.failed {
		return
	}
	failure := m.observationEvent(EvObservationFailed, reason)
	select {
	case m.Events <- failure:
		m.failed = true
		return
	default:
	}
	for draining := true; draining; {
		select {
		case <-m.Events:
		default:
			draining = false
		}
	}
	m.stale = true
	select {
	case m.Events <- m.observationEvent(EvObservationStale, "queued observations discarded after failure"):
	default:
		m.log.Warn("monitor: failed to queue stale observation after draining events", "reason", reason)
		return
	}
	select {
	case m.Events <- failure:
		m.failed = true
	default:
		m.log.Warn("monitor: failed to queue failed observation after draining events", "reason", reason)
	}
	select {
	case m.resync <- struct{}{}:
	default:
	}
}
