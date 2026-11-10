package pd

import (
	"net/netip"
	"slices"
	"testing"
)

func TestParseDescribePrefixesUsesDHCPv6Key(t *testing.T) {
	t.Parallel()

	fixture := []byte(`{
		"Name": "enmbrains0",
		"DHCPv6": {
			"Lease": {},
			"Prefixes": [
				{
					"Prefix": [38,7,245,152,211,232,69,0,0,0,0,0,0,0,0,0],
					"PrefixLength": 56
				}
			]
		}
	}`)

	prefixes, err := UnmarshalDescribePrefixes(fixture)
	if err != nil {
		t.Fatalf("UnmarshalDescribePrefixes returned error: %v", err)
	}
	want := netip.MustParsePrefix("2607:f598:d3e8:4500::/56")
	if len(prefixes) != 1 {
		t.Fatalf("len(prefixes)=%d, want 1", len(prefixes))
	}
	if prefixes[0] != want {
		t.Fatalf("prefix=%s, want %s", prefixes[0], want)
	}
}

func TestAddrFromPrefixBytes(t *testing.T) {
	t.Parallel()

	got, err := addrFromPrefixBytes(
		[]int{38, 7, 245, 152, 211, 232, 69, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	)
	if err != nil {
		t.Fatalf("addrFromPrefixBytes returned error: %v", err)
	}
	want := netip.MustParseAddr("2607:f598:d3e8:4500::")
	if got != want {
		t.Fatalf("addr=%s, want %s", got, want)
	}
}

func TestParseNetworkctlPrefixesFiltersLongPrefixes(t *testing.T) {
	t.Parallel()

	fixture := []byte(`{
		"Interfaces": [
			{
				"DelegatedPrefixes": [
					"2607:f598:d3e8:4500::/56",
					"2001:db8:1::/64"
				],
				"DelegatedPrefix": "2607:f598:d3e8:4600::/60"
			}
		]
	}`)

	got, err := UnmarshalNetworkctlPrefixes(fixture)
	if err != nil {
		t.Fatalf("UnmarshalNetworkctlPrefixes returned error: %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("2607:f598:d3e8:4500::/56"),
		netip.MustParsePrefix("2607:f598:d3e8:4600::/60"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("prefixes=%v, want %v", got, want)
	}
}

func TestParseNetworkctlPrefixesScanFallbackWithoutDelegatedKeys(t *testing.T) {
	t.Parallel()

	// No DelegatedPrefix* key is present, so the generic scan fallback must
	// still find the short IPv6 CIDR and skip the on-link /64, matching the
	// last resort in find-pd-prefixes.sh.
	fixture := []byte(`{
		"Name": "enmbrains0",
		"SomeVaryingKey": "2607:f598:d3e8:4500::/56",
		"Addresses": ["2607:f598:d3e8:4500:1::/64"]
	}`)

	got, err := UnmarshalNetworkctlPrefixes(fixture)
	if err != nil {
		t.Fatalf("UnmarshalNetworkctlPrefixes returned error: %v", err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("2607:f598:d3e8:4500::/56"),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("prefixes=%v, want %v", got, want)
	}
}
