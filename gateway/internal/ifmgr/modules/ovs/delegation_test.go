package ovs

// A delegated NPTv6 mapping uses the current DHCPv6 prefix and the configured
// external prefix length. A live /48 assignment supplies a /60 mapping.
// An expired assignment and a /64 assignment both leave the external prefix
// unset. Resolution preserves the configured mapping.

import (
	"net/netip"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

func TestDelegatedMappingUsesCurrentAssignment(t *testing.T) {
	const connection = "isp-b"
	internal := netip.MustParsePrefix("fd01:203:405::/56")
	expected := netip.MustParsePrefix("2001:db8:1234::/60")
	cfg := ovsconfig.OpenFlowConfig{
		Mappings: map[string]config.NPTv6Translation{
			connection: {
				InternalPrefix: internal,
				ExternalSource: config.PrefixDelegated,
				ExpectedPrefix: expected,
			},
		},
	}
	if !hasLeaseValues(&cfg) {
		t.Fatal("reconciliation would skip the WAN lease snapshot")
	}

	now := time.Unix(100, 0)
	expires := now.Add(time.Hour)
	var state wanstate.ConnectionState
	assignment := interfaceintent.Assignment{
		Kind:       interfaceintent.AssignmentDHCPv6IAPD,
		Value:      netip.MustParsePrefix("2001:db8:4567::/48"),
		Valid:      true,
		ValidUntil: &expires,
	}
	state.IPv6.Assignments = []interfaceintent.Assignment{assignment}
	values := leaseValues{
		connections: map[string]wanstate.ConnectionState{connection: state},
		now:         now,
	}
	check := func(want netip.Prefix) {
		t.Helper()
		resolved, err := values.openFlow(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}

		got, found := resolved.Mappings[connection]
		if !found || got.InternalPrefix != internal || got.ExternalPrefix != want {
			t.Fatalf("resolved mapping = %+v, want %s to %s", got, internal, want)
		}

		if cfg.Mappings[connection].ExternalPrefix.IsValid() {
			t.Fatal("lease resolution modified the configured mapping")
		}
	}
	check(netip.MustParsePrefix("2001:db8:4567::/60"))
	values.now = expires
	check(netip.Prefix{})
	values.now = now
	state.IPv6.Assignments[0].Value = netip.MustParsePrefix("2001:db8:9876::/64")
	check(netip.Prefix{})
}
