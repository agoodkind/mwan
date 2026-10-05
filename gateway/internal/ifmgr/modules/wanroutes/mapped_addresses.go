package wanroutes

import (
	"net/netip"
	"slices"

	"goodkind.io/mwan/internal/interfaceintent"
)

// Callers must lock the module before address reconciliation.
func (m *Module) ownMappedAddressesLocked() {
	owned := make(map[string][]netip.Addr, len(m.cfg.WANs))
	for _, wan := range m.cfg.WANs {
		if m.Env != nil && slices.ContainsFunc(m.Env.Connections, func(connection interfaceintent.Connection) bool {
			return connection.ID.String() == wan.Key() && connection.Owner == interfaceintent.OwnerExternal
		}) {
			continue
		}
		for _, address := range append(slices.Clone(wan.MappedExternals), wan.LocalMappedExternals...) {
			prefix := netip.PrefixFrom(address, hostPrefixBitsV4).String()
			if m.Env != nil && m.Env.OwnedAddresses != nil && m.Env.OwnedAddresses.Has(wan.Key(), prefix) {
				owned[wan.Key()] = append(owned[wan.Key()], address)
			}
		}
	}
	m.ownedAddresses = owned
}
