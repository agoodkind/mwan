package netif

import (
	"context"
	"time"
)

// publishSnapshot serializes snapshot replay with all netlink deltas.
func (m *Monitor) publishSnapshot(ctx context.Context, event Event, epoch uint64) bool {
	for {
		m.dispatchMu.Lock()
		m.mu.Lock()
		m.stateMu.Lock()
		if ctx.Err() != nil {
			m.stateMu.Unlock()
			m.mu.Unlock()
			m.dispatchMu.Unlock()
			return false
		}
		if m.bindingEpoch != epoch || !m.ready[0] || !m.ready[1] || !m.ready[2] {
			m.markStaleLocked()
			m.stateMu.Unlock()
			m.mu.Unlock()
			m.dispatchMu.Unlock()
			return false
		}
		select {
		case m.Events <- event:
			if m.ifIndex != event.IfIndex || m.actualIface != event.ActualIface {
				m.ifIndex = event.IfIndex
				m.actualIface = event.ActualIface
				m.bindingEpoch++
			}
			if m.dirty {
				m.dirty = false
				m.markStaleLocked()
			} else {
				m.stale = false
			}
			replay := !m.stale
			m.stateMu.Unlock()
			m.mu.Unlock()
			if replay {
				m.replaySnapshot(ctx, event.Snapshot)
			}
			m.dispatchMu.Unlock()
			return true
		default:
			m.stateMu.Unlock()
			m.mu.Unlock()
			m.dispatchMu.Unlock()
			if !sleepMonitorRetry(ctx, 10*time.Millisecond) {
				return false
			}
		}
	}
}

// replaySnapshot requires dispatchMu so a deletion cannot precede a replayed addition.
func (m *Monitor) replaySnapshot(ctx context.Context, snapshot *Snapshot) {
	if snapshot.IfIndex == 0 {
		return
	}
	var base Event
	base.Iface = snapshot.Iface
	base.ConnectionID = snapshot.ConnectionID
	base.ActualIface = snapshot.ActualIface
	base.IfIndex = snapshot.IfIndex
	base.ObservedAt = snapshot.ObservedAt
	base.SnapshotReplay = true
	if snapshot.LinkUp {
		event := base
		event.Kind = EvLinkUp
		m.emit(ctx, event)
	}
	for _, address := range snapshot.Addresses {
		event := base
		event.Kind = EvAddrAdded
		event.Family = address.Family
		event.CIDR = address.CIDR
		event.Flags = address.Flags
		event.Scope = address.Scope
		event.PreferredLifetime = address.PreferredLifetime
		event.ValidLifetime = address.ValidLifetime
		event.Origin = address.Origin
		m.emit(ctx, event)
	}
	for _, route := range snapshot.Routes {
		event := base
		event.Kind = EvRouteAdded
		event.Family = route.Family
		event.Dest = route.Dest
		event.Via = route.Via
		event.Dev = route.Dev
		event.TableID = route.TableID
		event.Protocol = route.Protocol
		event.Metric = route.Metric
		event.Scope = route.Scope
		event.Type = route.Type
		event.NextHops = route.NextHops
		m.emit(ctx, event)
	}
}
