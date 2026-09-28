package wanstate

import (
	"net/netip"
	"testing"
	"time"

	"goodkind.io/mwan/internal/interfaceintent"
)

func TestAssignmentSnapshotCopiesDeadlinesAndBoundsRecentHistory(t *testing.T) {
	store := New()
	store.SetTransitionLogger("run", nil)
	store.SetConnections([]interfaceintent.Connection{{ID: "provider", Name: "enprovider0", Owner: interfaceintent.OwnerNetworkd}})
	deadline := time.Now().UTC().Add(time.Hour)
	assignment := interfaceintent.Assignment{ConnectionID: "provider", Family: "ipv6", Kind: interfaceintent.AssignmentDHCPv6IANA, Source: "dhcpv6", Purpose: interfaceintent.PurposeLocal, Value: netip.MustParsePrefix("2001:db8::1/128"), ClientID: "", DUID: "", IAID: nil, AcquiredAt: time.Now().UTC(), RenewAt: &deadline, RebindAt: nil, PreferredUntil: nil, ValidUntil: &deadline, Valid: true}
	input := []interfaceintent.Assignment{assignment}
	store.SetAssignment("provider", "ipv6", "acquired", "valid", input)
	input[0].Value = netip.MustParsePrefix("2001:db8::2/128")
	deadline = deadline.Add(time.Hour)
	first := store.Snapshot()
	got := first.Connections["provider"].IPv6.Assignments[0]
	if got.Value != assignment.Value || got.ValidUntil.Equal(deadline) {
		t.Fatalf("store retained mutable assignment input: %+v", got)
	}
	*got.ValidUntil = time.Time{}
	second := store.Snapshot().Connections["provider"].IPv6.Assignments[0]
	if second.ValidUntil.IsZero() {
		t.Fatal("snapshot changed stored deadline")
	}
	for i := 0; i < 40; i++ {
		readiness := "ready"
		if i%2 == 1 {
			readiness = "not-ready"
		}
		store.SetReadiness("provider", "ipv6", readiness)
	}
	history := store.Snapshot().Connections["provider"].Recent
	if len(history) != RecentTransitionLimit {
		t.Fatalf("history size = %d", len(history))
	}
	if history[0].ID != "run:10" || history[len(history)-1].ID != "run:41" {
		t.Fatalf("history identities = %s..%s", history[0].ID, history[len(history)-1].ID)
	}
}
