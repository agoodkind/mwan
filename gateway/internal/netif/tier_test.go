package netif

import "testing"

func TestActiveTier(t *testing.T) {
	t.Parallel()

	members := []TierMember{
		{Name: "att", Tier: 0},
		{Name: "webpass", Tier: 0},
		{Name: "monkeybrains", Tier: 1},
		{Name: "astount", Tier: 2},
	}
	cases := []struct {
		name        string
		health      HealthStates
		wantTier    uint8
		wantHealthy bool
	}{
		{
			name:        "no verdict recorded reads healthy and activates the first tier",
			health:      HealthStates{},
			wantTier:    0,
			wantHealthy: true,
		},
		{
			name: "one healthy member in the first tier keeps it active",
			health: HealthStates{
				"att": HealthStateUnhealthy, "webpass": HealthStateHealthy,
				"monkeybrains": HealthStateHealthy, "astount": HealthStateHealthy,
			},
			wantTier:    0,
			wantHealthy: true,
		},
		{
			name: "the first tier going unhealthy activates the next one that is not",
			health: HealthStates{
				"att": HealthStateUnhealthy, "webpass": HealthStateUnhealthy,
				"monkeybrains": HealthStateHealthy, "astount": HealthStateHealthy,
			},
			wantTier:    1,
			wantHealthy: true,
		},
		{
			name: "an empty tier is skipped rather than activated",
			health: HealthStates{
				"att": HealthStateUnhealthy, "webpass": HealthStateUnhealthy,
				"monkeybrains": HealthStateUnhealthy, "astount": HealthStateHealthy,
			},
			wantTier:    2,
			wantHealthy: true,
		},
		{
			name: "no healthy member anywhere activates no tier",
			health: HealthStates{
				"att": HealthStateUnhealthy, "webpass": HealthStateUnhealthy,
				"monkeybrains": HealthStateUnhealthy, "astount": HealthStateUnhealthy,
			},
			wantTier:    0,
			wantHealthy: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotTier, gotHealthy := ActiveTier(members, tc.health)
			if gotHealthy != tc.wantHealthy {
				t.Fatalf("healthy = %v, want %v", gotHealthy, tc.wantHealthy)
			}
			if gotHealthy && gotTier != tc.wantTier {
				t.Fatalf("active tier = %d, want %d", gotTier, tc.wantTier)
			}
		})
	}
}

func TestActiveTierWithNoMembers(t *testing.T) {
	t.Parallel()

	if _, healthy := ActiveTier(nil, HealthStates{}); healthy {
		t.Fatal("an empty member list reported a healthy tier")
	}
}

// TestActiveTierPicksTheLowestTierRegardlessOfMemberOrder pins that the
// function selects by tier value, not by encounter order. Production feeds
// members in name order (att, monkeybrains, webpass), which does not match
// ascending tier order the way TestActiveTier's shared fixture happens to; a
// healthy tier-1 member encountered before a healthy tier-0 member must not
// win just because it was seen first.
func TestActiveTierPicksTheLowestTierRegardlessOfMemberOrder(t *testing.T) {
	t.Parallel()

	members := []TierMember{
		{Name: "att", Tier: 0},
		{Name: "monkeybrains", Tier: 1},
		{Name: "webpass", Tier: 0},
	}
	health := HealthStates{
		"att":          HealthStateUnhealthy,
		"monkeybrains": HealthStateHealthy,
		"webpass":      HealthStateHealthy,
	}

	gotTier, gotHealthy := ActiveTier(members, health)
	if !gotHealthy {
		t.Fatal("healthy = false, want true")
	}
	if gotTier != 0 {
		t.Fatalf("active tier = %d, want 0", gotTier)
	}
}
