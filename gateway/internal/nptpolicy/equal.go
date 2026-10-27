package nptpolicy

import (
	"net/netip"
	"slices"
)

// EqualPair compares the mapping and exception sets without changing either input.
func EqualPair(previous, next PrefixPair) bool {
	return previous.ID == next.ID && previous.Internal == next.Internal && previous.External == next.External &&
		equalExceptions(previous.SourceExceptions, next.SourceExceptions) &&
		equalExceptions(previous.DestinationExceptions, next.DestinationExceptions)
}

func equalExceptions(previous, next []netip.Addr) bool {
	first, second := slices.Clone(previous), slices.Clone(next)
	slices.SortFunc(first, netip.Addr.Compare)
	slices.SortFunc(second, netip.Addr.Compare)
	return slices.Equal(slices.Compact(first), slices.Compact(second))
}
