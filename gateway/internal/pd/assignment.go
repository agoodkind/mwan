package pd

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

// AssignmentSource selects negotiated leases for owned links and the legacy
// observer for links still managed by networkd.
type AssignmentSource struct {
	owners   map[string]interfaceintent.Owner
	leases   *netif.DHCPv6PDStore
	fallback Source
	clock    clock.Clock
}

// NewAssignmentSource routes reads by connection owner.
func NewAssignmentSource(connections []interfaceintent.Connection, leases *netif.DHCPv6PDStore, fallback Source, currentClock clock.Clock) *AssignmentSource {
	owners := make(map[string]interfaceintent.Owner, len(connections))
	for _, connection := range connections {
		owners[connection.Name] = connection.Owner
	}
	return &AssignmentSource{owners: owners, leases: leases, fallback: fallback, clock: currentClock}
}

// Prefix returns a currently valid negotiated prefix for an owned interface.
func (source *AssignmentSource) Prefix(ctx context.Context, iface string) (netip.Prefix, bool, error) {
	if source.owners[iface] != interfaceintent.OwnerMWAN {
		prefix, ok, err := source.fallback.Prefix(ctx, iface)
		if err != nil {
			slog.WarnContext(ctx, "pd: networkd prefix observation failed", "iface", iface, "err", err)
			return netip.Prefix{}, false, fmt.Errorf("observe delegated prefix: %w", err)
		}
		return prefix, ok, nil
	}
	if source.leases == nil {
		return netip.Prefix{}, false, nil
	}
	lease, ok := source.leases.Get(iface)
	if !ok {
		return netip.Prefix{}, false, nil
	}
	now := source.clock.Now()
	for _, prefix := range lease.Prefixes {
		if now.Before(prefix.ValidUntil) {
			return prefix.Prefix, true, nil
		}
	}
	return netip.Prefix{}, false, nil
}
