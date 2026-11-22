package statuspush_test

import (
	"maps"
	"testing"
	"time"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/statuspush"
)

func TestNewStatusPicksTheLowestHealthyTier(t *testing.T) {
	t.Parallel()

	members := []netif.TierMember{
		{Name: "webpass", Tier: 0},
		{Name: "att", Tier: 1},
		{Name: "monkeybrains", Tier: 2},
	}
	states := netif.HealthStates{
		"webpass":      "unhealthy",
		"att":          "unhealthy",
		"monkeybrains": "healthy",
		"unlisted":     "healthy",
	}
	sentAt := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

	status := statuspush.NewStatus(sentAt, members, states)

	if status.ActiveTier != 2 {
		t.Fatalf("active tier = %d, want 2", status.ActiveTier)
	}

	if !status.SentAt.Equal(sentAt) {
		t.Fatalf("sent_at = %s, want %s", status.SentAt, sentAt)
	}

	want := map[string]string{
		"webpass":      "unhealthy",
		"att":          "unhealthy",
		"monkeybrains": "healthy",
	}
	if !maps.Equal(status.Providers, want) {
		t.Fatalf("providers = %v, want %v", status.Providers, want)
	}
}

func TestNewStatusReadsAnUnrecognizedVerdictAsUnknown(t *testing.T) {
	t.Parallel()

	members := []netif.TierMember{{Name: "webpass", Tier: 0}, {Name: "att", Tier: 1}}
	states := netif.HealthStates{"webpass": "garbled"}

	status := statuspush.NewStatus(time.Time{}, members, states)

	if got := status.Providers["webpass"]; got != "unknown" {
		t.Fatalf("webpass verdict = %q, want unknown", got)
	}

	if got := status.Providers["att"]; got != "unknown" {
		t.Fatalf("att verdict = %q, want unknown", got)
	}

	if status.ActiveTier != 0 {
		t.Fatalf("active tier = %d, want 0 because unknown reads healthy", status.ActiveTier)
	}
}
