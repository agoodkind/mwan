package pinned

import (
	"net/netip"
	"testing"
)

func rangeStrings(ranges []addrRange) []string {
	out := make([]string, 0, len(ranges))
	for _, r := range ranges {
		out = append(out, r.start.String()+"-"+r.end.String())
	}
	return out
}

// TestMergePrefixesRemovesOverlap is the property the kernel forces on this
// module: a host address inside a range it also holds must not become its own
// element, because the interval backend refuses an element overlapping one
// already in the same transaction. The shell refresher leaned on nft's own
// merging for this; here it is done before anything is written.
func TestMergePrefixesRemovesOverlap(t *testing.T) {
	t.Parallel()

	merged := mergePrefixes(mustPrefixes(t,
		"208.54.0.0/16",
		"208.54.1.2/32",
		"208.54.0.0/16",
		"208.54.128.0/17",
	))
	assertSequence(t, rangeStrings(merged), []string{"208.54.0.0-208.54.255.255"})
}

// TestMergePrefixesJoinsAdjacentRanges keeps two halves of one range from
// being written as two abutting element pairs.
func TestMergePrefixesJoinsAdjacentRanges(t *testing.T) {
	t.Parallel()

	merged := mergePrefixes(mustPrefixes(t, "10.0.0.128/25", "10.0.0.0/25"))
	assertSequence(t, rangeStrings(merged), []string{"10.0.0.0-10.0.0.255"})
}

// TestMergePrefixesKeepsDisjointRangesApartAndSorted checks that ranges that
// do not touch stay separate, in ascending order.
func TestMergePrefixesKeepsDisjointRangesApartAndSorted(t *testing.T) {
	t.Parallel()

	merged := mergePrefixes(mustPrefixes(t,
		"198.51.100.0/24",
		"10.0.0.0/8",
		"192.0.2.128/25",
	))
	assertSequence(t, rangeStrings(merged), []string{
		"10.0.0.0-10.255.255.255",
		"192.0.2.128-192.0.2.255",
		"198.51.100.0-198.51.100.255",
	})
}

// TestMergePrefixesHandlesIPv6 covers the wider family, including a range that
// ends at the top of the key space.
func TestMergePrefixesHandlesIPv6(t *testing.T) {
	t.Parallel()

	merged := mergePrefixes(mustPrefixes(t, "2600:1000::/28", "2600:1000:abcd::1/128", "2001:db8::/32"))
	assertSequence(t, rangeStrings(merged), []string{
		"2001:db8::-2001:db8:ffff:ffff:ffff:ffff:ffff:ffff",
		"2600:1000::-2600:100f:ffff:ffff:ffff:ffff:ffff:ffff",
	})

	whole := mergePrefixes(mustPrefixes(t, "::/0"))
	assertSequence(t, rangeStrings(whole), []string{"::-ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff"})
}

// TestPrefixRangeCoversTheWholePrefix checks the two ends of a prefix, which
// is what every element pair is derived from.
func TestPrefixRangeCoversTheWholePrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		prefix string
		want   string
	}{
		{prefix: "192.0.2.0/24", want: "192.0.2.0-192.0.2.255"},
		{prefix: "192.0.2.7/32", want: "192.0.2.7-192.0.2.7"},
		{prefix: "0.0.0.0/0", want: "0.0.0.0-255.255.255.255"},
		{prefix: "74.125.250.0/24", want: "74.125.250.0-74.125.250.255"},
		{prefix: "3.7.35.0/25", want: "3.7.35.0-3.7.35.127"},
		{prefix: "2001:db8::/64", want: "2001:db8::-2001:db8::ffff:ffff:ffff:ffff"},
		{prefix: "2600:1000::/28", want: "2600:1000::-2600:100f:ffff:ffff:ffff:ffff:ffff:ffff"},
	}
	for _, test := range tests {
		t.Run(test.prefix, func(t *testing.T) {
			t.Parallel()

			prefix, err := netip.ParsePrefix(test.prefix)
			if err != nil {
				t.Fatalf("parse %q: %v", test.prefix, err)
			}
			got := prefixRange(prefix)
			if text := got.start.String() + "-" + got.end.String(); text != test.want {
				t.Fatalf("prefixRange(%s) = %s, want %s", test.prefix, text, test.want)
			}
		})
	}
}

// TestParsePrefixesRefusesTheWrongFamily proves a misplaced range fails
// startup rather than disappearing from every refresh.
func TestParsePrefixesRefusesTheWrongFamily(t *testing.T) {
	t.Parallel()

	if _, err := parsePrefixes([]string{"2001:db8::/32"}, familyV4, "seed_cidrs_v4"); err == nil {
		t.Fatal("an IPv6 range in the IPv4 seed list was accepted")
	}
	if _, err := parsePrefixes([]string{"192.0.2.0/24"}, familyV6, "seed_cidrs_v6"); err == nil {
		t.Fatal("an IPv4 range in the IPv6 seed list was accepted")
	}
	if _, err := parsePrefixes([]string{"not-a-range"}, familyV4, "seed_cidrs_v4"); err == nil {
		t.Fatal("an unparsable seed range was accepted")
	}
}

// TestHostPrefixKeepsTheFamilySeparate proves a v4-mapped answer never reaches
// the IPv6 set, whose keys are sixteen bytes wide.
func TestHostPrefixKeepsTheFamilySeparate(t *testing.T) {
	t.Parallel()

	mapped := netip.MustParseAddr("::ffff:192.0.2.7")
	prefix, ok := hostPrefix(mapped, familyV4)
	if !ok || prefix.String() != "192.0.2.7/32" {
		t.Fatalf("hostPrefix(mapped, ipv4) = %s, %t, want 192.0.2.7/32, true", prefix, ok)
	}
	if _, ok := hostPrefix(mapped, familyV6); ok {
		t.Fatal("a v4-mapped answer was accepted into the IPv6 set")
	}
	if _, ok := hostPrefix(netip.MustParseAddr("2001:db8::1"), familyV4); ok {
		t.Fatal("an IPv6 answer was accepted into the IPv4 set")
	}
}
