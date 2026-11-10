package netif

import "github.com/vishvananda/netlink"

func (m *Monitor) watchesIndex(index int) bool {
	if index == 0 {
		return false
	}

	m.mu.Lock()
	previousIndex := m.ifIndex
	previousIface := m.actualIface
	if m.cfg.Connection != nil {
		m.rebindConfiguredLocked()
		matched := index == m.ifIndex && m.ifIndex != 0
		changed := previousIndex != m.ifIndex || previousIface != m.actualIface
		if changed {
			m.bindingEpoch++
		}

		m.mu.Unlock()
		if changed {
			m.invalidateBinding()
		}

		return matched
	}

	m.rebindLegacyLocked(index)
	matched := index == m.ifIndex && m.ifIndex != 0
	changed := previousIndex != m.ifIndex || previousIface != m.actualIface
	if changed {
		m.bindingEpoch++
	}

	m.mu.Unlock()
	if changed {
		m.invalidateBinding()
	}

	return matched
}

func (m *Monitor) rebindLegacyLocked(index int) {
	if m.ifIndex != 0 {
		link, err := netlink.LinkByIndex(m.ifIndex)
		if err != nil || link.Attrs() == nil || link.Attrs().Name != m.cfg.Iface {
			m.ifIndex = 0
			m.actualIface = ""
		}
	}

	if m.ifIndex != 0 || index == 0 {
		return
	}

	link, err := netlink.LinkByIndex(index)
	if err != nil || link.Attrs() == nil || link.Attrs().Name != m.cfg.Iface {
		return
	}

	m.ifIndex = index
	m.actualIface = link.Attrs().Name
}

func (m *Monitor) invalidateBinding() {
	m.stateMu.Lock()
	m.markStaleLocked("link binding changed")
	m.stateMu.Unlock()
}

func (m *Monitor) rebindConfiguredLocked() {
	link, err := resolveConnectionLink(m.log, *m.cfg.Connection)
	if err != nil {
		m.log.Warn("monitor: configured link match failed", "err", err)
	}

	if err != nil || link == nil {
		m.ifIndex = 0
		m.actualIface = ""

		return
	}

	m.ifIndex = link.Attrs().Index
	m.actualIface = link.Attrs().Name
}
