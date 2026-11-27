package ovs

// Lease binding provides the IPv6 prefix translation compiler with the current
// external prefix for mappings that use DHCPv6 prefix delegation. For example,
// it can select the first /64 of a delegated /56 for a connection's mapping.

import (
	"context"
	"log/slog"
	"maps"
	"net/netip"

	"goodkind.io/mwan/gateway/internal/config"
)

func (v leaseValues) resolveMappings(
	ctx context.Context,
	mappings map[string]config.NPTv6Translation,
) map[string]config.NPTv6Translation {
	resolved := maps.Clone(mappings)
	for connectionID, mapping := range mappings {
		if mapping.ExternalSource != config.PrefixDelegated {
			continue
		}

		mapping.ExternalPrefix = v.externalPrefix(ctx, connectionID, mapping)
		resolved[connectionID] = mapping
	}

	return resolved
}

func (v leaseValues) externalPrefix(
	ctx context.Context,
	connectionID string,
	mapping config.NPTv6Translation,
) netip.Prefix {
	prefix, err := v.assignment(ipv6Delegation, connectionID)
	if err != nil {
		slog.WarnContext(
			ctx,
			"NPTv6 delegated prefix unavailable",
			"connection",
			connectionID,
			"err",
			err,
		)

		return netip.Prefix{}
	}

	bits := mapping.InternalPrefix.Bits()
	if mapping.ExpectedPrefix.IsValid() {
		bits = mapping.ExpectedPrefix.Bits()
	}

	if prefix.Bits() > bits {
		slog.WarnContext(
			ctx,
			"Delegated IPv6 prefix is too small for NPTv6",
			"connection",
			connectionID,
			"prefix",
			prefix,
			"required_bits",
			bits,
		)

		return netip.Prefix{}
	}

	return netip.PrefixFrom(prefix.Addr(), bits).Masked()
}
