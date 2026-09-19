package wanstate

import (
	"fmt"
	"net/netip"
	"testing"
	"time"
)

// TestStore_SnapshotCarriesEveryWrite pins the store round-trip: what the
// modules write is exactly what a provider's snapshot reads.
func TestStore_SnapshotCarriesEveryWrite(t *testing.T) {
	t.Parallel()
	store := New()
	transition := time.Date(2026, 8, 20, 1, 2, 3, 0, time.UTC)
	store.SetHealth(map[string]MemberHealth{
		"att": {
			Verdict: HealthHealthy, ConsecutiveFailures: 0,
			LastTransition: transition, V4: ProbePass, V6: ProbePass,
		},
	})
	store.SetRouting(1, map[string]MemberRouting{
		"att":          {Carrying: false},
		"monkeybrains": {Carrying: true},
	})
	store.SetTranslation(map[string]MemberTranslation{
		"att": {Delegated: netip.MustParsePrefix("2001:db8:a::/60"), KernelPresent: true},
	})
	store.SetBGP(BGP{
		Peers:   []BGPPeer{{Address: "2001:db8::1", Established: true}},
		ReadAt:  transition,
		Reached: true,
	})

	snap := store.Snapshot()
	if snap.Health["att"].Verdict != HealthHealthy || snap.Health["att"].V6 != ProbePass {
		t.Fatalf("health snapshot = %+v", snap.Health["att"])
	}
	if !snap.TierValid || snap.ActiveTier != 1 {
		t.Fatalf("tier snapshot = valid=%v tier=%d", snap.TierValid, snap.ActiveTier)
	}
	if !snap.Routing["monkeybrains"].Carrying || snap.Routing["att"].Carrying {
		t.Fatalf("routing snapshot = %+v", snap.Routing)
	}
	if !snap.Translation["att"].KernelPresent ||
		snap.Translation["att"].Delegated.String() != "2001:db8:a::/60" {
		t.Fatalf("translation snapshot = %+v", snap.Translation["att"])
	}
	if !snap.BGP.Reached || len(snap.BGP.Peers) != 1 || !snap.BGP.Peers[0].Established {
		t.Fatalf("bgp snapshot = %+v", snap.BGP)
	}
}

// recordingObserver collects the events the store forwards.
type recordingObserver struct {
	health []string
	tiers  []string
}

func (o *recordingObserver) HealthTransition(member string, from Health, to Health) {
	o.health = append(o.health, member+":"+string(from)+">"+string(to))
}

func (o *recordingObserver) TierChange(from uint8, to uint8) {
	o.tiers = append(o.tiers, fmt.Sprintf("%d>%d", from, to))
}

// TestStore_TierChangeReachesTheObserver pins the tier seam: the first
// valid write is a baseline and stays silent, a write with the same tier
// stays silent, and only a write that installs a different tier reaches
// the observer with both tiers.
func TestStore_TierChangeReachesTheObserver(t *testing.T) {
	t.Parallel()
	store := New()
	observer := &recordingObserver{health: nil, tiers: nil}
	store.Observe(observer)

	store.SetRouting(0, map[string]MemberRouting{"att": {Carrying: true}})
	if len(observer.tiers) != 0 {
		t.Fatalf("baseline write produced events: %v", observer.tiers)
	}
	store.SetRouting(0, map[string]MemberRouting{"att": {Carrying: true}})
	if len(observer.tiers) != 0 {
		t.Fatalf("same-tier write produced events: %v", observer.tiers)
	}
	store.SetRouting(1, map[string]MemberRouting{"att": {Carrying: false}})
	if len(observer.tiers) != 1 || observer.tiers[0] != "0>1" {
		t.Fatalf("tier events = %v, want [0>1]", observer.tiers)
	}
}

// TestStore_HealthTransitionForwarded pins the health seam: the health
// module's committed transition reaches the observer unchanged, and a
// store without an observer swallows it.
func TestStore_HealthTransitionForwarded(t *testing.T) {
	t.Parallel()
	store := New()
	store.NotifyHealthTransition("att", HealthUnknown, HealthHealthy)

	observer := &recordingObserver{health: nil, tiers: nil}
	store.Observe(observer)
	store.NotifyHealthTransition("att", HealthHealthy, HealthUnhealthy)
	if len(observer.health) != 1 || observer.health[0] != "att:healthy>unhealthy" {
		t.Fatalf("health events = %v, want [att:healthy>unhealthy]", observer.health)
	}
}

// TestStore_SnapshotIsIsolated pins that mutating a snapshot or the maps
// handed to the setters never reaches the store, so a provider read can
// never corrupt what the next reader sees.
func TestStore_SnapshotIsIsolated(t *testing.T) {
	t.Parallel()
	store := New()
	written := map[string]MemberHealth{"att": {Verdict: HealthHealthy}}
	store.SetHealth(written)
	written["att"] = MemberHealth{Verdict: HealthUnhealthy}

	first := store.Snapshot()
	first.Health["att"] = MemberHealth{Verdict: HealthUnknown}
	first.BGP.Peers = append(first.BGP.Peers, BGPPeer{Address: "intruder"})

	second := store.Snapshot()
	if second.Health["att"].Verdict != HealthHealthy {
		t.Fatalf("store mutated through a snapshot or setter map: %+v", second.Health["att"])
	}
	if len(second.BGP.Peers) != 0 {
		t.Fatalf("bgp peers mutated through a snapshot: %+v", second.BGP.Peers)
	}
}
