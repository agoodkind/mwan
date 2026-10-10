package wanroutes

func (m *Module) excludeRouteFamily(current gateways, tableID int, family string) {
	for _, wan := range m.cfg.WANs {
		if wan.TableID != tableID {
			continue
		}
		gateway := current[wan.Key()]
		if family == familyV4 {
			gateway.V4 = ""
		} else {
			gateway = gateway.withoutV6()
		}
		current[wan.Key()] = gateway
	}
}

func (m *Module) excludeUnreadyOwnedFamilies(current gateways) {
	for _, wan := range m.cfg.WANs {
		if !wan.Owned {
			continue
		}
		gateway := current[wan.Key()]
		if m.Env == nil || m.Env.OwnedAddresses == nil || !m.Env.OwnedAddresses.FamilyReady(wan.Key(), "ipv4") {
			gateway.V4 = ""
		}
		if m.Env == nil || m.Env.OwnedAddresses == nil || !m.Env.OwnedAddresses.FamilyReady(wan.Key(), "ipv6") {
			gateway = gateway.withoutV6()
		}
		current[wan.Key()] = gateway
	}
}
