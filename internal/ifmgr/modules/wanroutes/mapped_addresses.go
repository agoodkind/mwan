package wanroutes

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"slices"

	"goodkind.io/mwan/internal/interfaceintent"
)

// Callers must lock the module before address reconciliation.
func (m *Module) ownMappedAddressesLocked(ctx context.Context, log *slog.Logger) error {
	owned := make(map[string][]netip.Addr, len(m.cfg.WANs))
	var ownershipErr error
	for _, wan := range m.cfg.WANs {
		if m.Env != nil && slices.ContainsFunc(m.Env.Connections, func(connection interfaceintent.Connection) bool {
			return connection.ID.String() == wan.Key() && connection.Owner == interfaceintent.OwnerExternal
		}) {
			continue
		}
		if wan.Owned {
			for _, address := range append(slices.Clone(wan.MappedExternals), wan.LocalMappedExternals...) {
				prefix := netip.PrefixFrom(address, hostPrefixBitsV4).String()
				if m.Env != nil && m.Env.OwnedAddresses != nil && m.Env.OwnedAddresses.Has(wan.Key(), prefix) {
					owned[wan.Key()] = append(owned[wan.Key()], address)
				}
			}
			continue
		}
		if len(wan.MappedExternals) == 0 && len(wan.LocalMappedExternals) == 0 {
			continue
		}
		addresses, err := m.ownLinkAddresses(ctx, log, wan)
		if err != nil {
			ownershipErr = errors.Join(ownershipErr, err)
			continue
		}
		owned[wan.Key()] = addresses
	}
	m.ownedAddresses = owned
	return ownershipErr
}
