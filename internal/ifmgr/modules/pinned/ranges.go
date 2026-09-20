package pinned

import (
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
)

// family is one address family as this module handles it: it selects the set
// to write, the lookup network name, and the host-prefix length a resolved
// address becomes.
type family struct {
	name    string
	network string
	bits    int
}

var (
	familyV4 = family{name: "ipv4", network: "ip4", bits: 32}
	familyV6 = family{name: "ipv6", network: "ip6", bits: 128}
)

// holds reports whether addr belongs to this family. Every address is unmapped
// first, so a v4-mapped answer counts as IPv4 and never reaches the IPv6 set,
// whose keys are sixteen bytes wide.
func (f family) holds(addr netip.Addr) bool {
	if f.bits == familyV4.bits {
		return addr.Is4()
	}
	return addr.Is6()
}

// addrRange is one inclusive range of addresses of a single family. Ranges are
// what the kernel's interval sets store, and merging the configured prefixes
// into them here is what replaces the merging nft's command-line tool performs
// on the set's auto-merge flag before it hands elements to the kernel.
type addrRange struct {
	start netip.Addr
	end   netip.Addr
}

// desiredSets is one refresh's full intent: the merged ranges each set should
// store.
type desiredSets struct {
	V4 []addrRange
	V6 []addrRange
}

// parsePrefixes parses one configured CIDR list and refuses an entry of the
// wrong family, because an IPv6 range in the IPv4 list would be dropped
// silently on every refresh.
func parsePrefixes(values []string, fam family, fieldName string) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(values))
	for i, value := range values {
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			slog.Warn("pinned: parse seed CIDR failed",
				"field", fieldName, "index", i, "value", value, "err", err)
			return nil, fmt.Errorf("pinned: %s[%d] %q: %w", fieldName, i, value, err)
		}
		prefix = unmapPrefix(prefix)
		if !fam.holds(prefix.Addr()) {
			slog.Warn("pinned: seed CIDR is the wrong family",
				"field", fieldName, "index", i, "value", value, "want", fam.name)
			return nil, fmt.Errorf("pinned: %s[%d] %q is not %s", fieldName, i, value, fam.name)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

// unmapPrefix rewrites a v4-mapped IPv6 prefix as the plain IPv4 prefix it
// stands for, so one address has one representation everywhere below.
func unmapPrefix(prefix netip.Prefix) netip.Prefix {
	addr := prefix.Addr().Unmap()
	if addr == prefix.Addr() {
		return prefix
	}
	return netip.PrefixFrom(addr, prefix.Bits()-(prefix.Addr().BitLen()-addr.BitLen()))
}

// hostPrefix turns one resolved address into the single-address prefix the set
// holds, and reports false for an answer of the other family.
func hostPrefix(addr netip.Addr, fam family) (netip.Prefix, bool) {
	addr = addr.Unmap()
	if !addr.IsValid() || !fam.holds(addr) {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(addr, fam.bits), true
}

// mergePrefixes turns a list of prefixes, which may overlap and repeat, into
// the smallest ordered list of disjoint, non-adjacent ranges covering the same
// addresses. Overlap is not merely wasteful here: the kernel refuses a set
// element that overlaps one already in the same transaction, which is what
// made the shell refresher depend on the set's auto-merge flag.
func mergePrefixes(prefixes []netip.Prefix) []addrRange {
	ranges := make([]addrRange, 0, len(prefixes))
	for _, prefix := range prefixes {
		if !prefix.IsValid() {
			continue
		}
		ranges = append(ranges, prefixRange(prefix))
	}
	if len(ranges) == 0 {
		return nil
	}
	slices.SortFunc(ranges, func(a, b addrRange) int {
		if cmp := a.start.Compare(b.start); cmp != 0 {
			return cmp
		}
		return a.end.Compare(b.end)
	})

	merged := make([]addrRange, 0, len(ranges))
	current := ranges[0]
	for _, next := range ranges[1:] {
		if joins(current, next) {
			if next.end.Compare(current.end) > 0 {
				current.end = next.end
			}
			continue
		}
		merged = append(merged, current)
		current = next
	}
	return append(merged, current)
}

// joins reports whether next overlaps current or starts at the very next
// address after it. Adjacent ranges are merged as well as overlapping ones, so
// two halves of a range become one element pair rather than two abutting ones.
func joins(current, next addrRange) bool {
	if next.start.Compare(current.end) <= 0 {
		return true
	}
	after := current.end.Next()
	return after.IsValid() && next.start == after
}

// prefixRange returns the first and last address a prefix covers.
func prefixRange(prefix netip.Prefix) addrRange {
	prefix = prefix.Masked()
	start := prefix.Addr()
	octets := start.As16()
	hostBits := start.BitLen() - prefix.Bits()
	for bit := range hostBits {
		index := len(octets) - 1 - bit/8
		octets[index] |= 1 << (bit % 8)
	}
	end := netip.AddrFrom16(octets)
	if start.Is4() {
		end = end.Unmap()
	}
	return addrRange{start: start, end: end}
}
