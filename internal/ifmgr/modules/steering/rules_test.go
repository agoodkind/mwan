package steering

import (
	"net/netip"
	"reflect"
	"testing"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

// membersForTest is three providers in two tiers with equal weights: the shape
// the gateway runs today.
func membersForTest() []Member {
	return []Member{
		{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0.3242"}, Mark: 1, Tier: 0, Weight: 1},
		{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "enwebpass0"}, Mark: 2, Tier: 0, Weight: 1},
		{WANRef: ifmgr.WANRef{Name: "monkeybrains", Iface: "enmbrains0"}, Mark: 3, Tier: 1, Weight: 1},
	}
}

func TestBalancerForSpreadsTheActiveTier(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		members []Member
		health  netif.HealthStates
		want    balancer
		wantOK  bool
	}{
		{
			// Two healthy providers in the first tier, one slot each: the
			// half-and-half split the three lines in the ruleset file express.
			name:    "two equal providers in the first tier",
			members: membersForTest(),
			health: netif.HealthStates{
				"att": netif.HealthStateHealthy, "webpass": netif.HealthStateHealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			want:   balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}},
			wantOK: true,
		},
		{
			// Weight two and weight one: three slots, two of them the heavier
			// provider's mark, assigned in ascending mark order.
			name: "unequal weights take proportional slots",
			members: []Member{
				{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0.3242"}, Mark: 1, Tier: 0, Weight: 2},
				{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "enwebpass0"}, Mark: 2, Tier: 0, Weight: 1},
			},
			health: netif.HealthStates{
				"att": netif.HealthStateHealthy, "webpass": netif.HealthStateHealthy,
			},
			want:   balancer{Mark: 0, Modulus: 3, Slots: []uint32{1, 1, 2}},
			wantOK: true,
		},
		{
			// One healthy provider takes everything with no generator, whatever
			// its weight: there is nothing to divide.
			name:    "a lone healthy provider in the first tier",
			members: membersForTest(),
			health: netif.HealthStates{
				"att": netif.HealthStateHealthy, "webpass": netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			want:   balancer{Mark: 1, Modulus: 0, Slots: nil},
			wantOK: true,
		},
		{
			// The first tier is out, so the next tier that is not carries. It
			// holds one provider, so its mark is set outright. This is today's
			// Monkeybrains behavior.
			name:    "the fallback tier's lone provider",
			members: membersForTest(),
			health: netif.HealthStates{
				"att": netif.HealthStateUnhealthy, "webpass": netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			want:   balancer{Mark: 3, Modulus: 0, Slots: nil},
			wantOK: true,
		},
		{
			// Nothing is healthy anywhere, so no mark is assigned at all and
			// the chain is left empty.
			name:    "no healthy provider anywhere",
			members: membersForTest(),
			health: netif.HealthStates{
				"att": netif.HealthStateUnhealthy, "webpass": netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateUnhealthy,
			},
			want:   balancer{Mark: 0, Modulus: 0, Slots: nil},
			wantOK: false,
		},
		{
			// No verdict has been recorded, so every provider reads healthy and
			// the first tier spreads. This is the startup pass.
			name:    "no verdict recorded activates the first tier",
			members: membersForTest(),
			health:  netif.HealthStates{},
			want:    balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}},
			wantOK:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, ok := balancerFor(tc.members, tc.health)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("balancer mismatch\ngot:  %#v\nwant: %#v", got, tc.want)
			}
		})
	}
}

// TestBuildRulesCoversBothFamilies pins the three rules the module programs:
// internal IPv4 arriving on the internal link, the router's own IPv6 edge
// address, and the internal IPv6 prefix.
func TestBuildRulesCoversBothFamilies(t *testing.T) {
	t.Parallel()

	assign := balancer{Mark: 0, Modulus: 2, Slots: []uint32{1, 2}}
	rules := buildRules(ruleInput{
		InternalIface:  "enmwanbr0",
		InternalNetV4:  netip.MustParsePrefix("10.250.250.0/29"),
		InternalPrefix: netip.MustParsePrefix("3d06:bad:b01::/60"),
		OpnsenseEdgeV6: netip.MustParseAddr("3d06:bad:b01:201::1"),
		Mode:           hashModeRandom,
		Assign:         assign,
	})

	want := []steerRule{
		{
			IifName: "enmwanbr0",
			Source:  netip.MustParsePrefix("10.250.250.0/29"),
			Mode:    hashModeRandom,
			Assign:  assign,
		},
		{
			IifName: "",
			Source:  netip.MustParsePrefix("3d06:bad:b01:201::1/128"),
			Mode:    hashModeRandom,
			Assign:  assign,
		},
		{
			IifName: "",
			Source:  netip.MustParsePrefix("3d06:bad:b01::/60"),
			Mode:    hashModeRandom,
			Assign:  assign,
		},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules mismatch\ngot:  %#v\nwant: %#v", rules, want)
	}
}
