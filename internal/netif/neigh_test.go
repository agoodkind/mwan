package netif

import (
	"testing"

	"golang.org/x/sys/unix"
)

// Neighbour states with a usable link-layer address count as resolved;
// INCOMPLETE, FAILED, and NONE mean a route via the entry black-holes.
func TestNeighStateResolved(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		state    int
		resolved bool
	}{
		{name: "reachable", state: unix.NUD_REACHABLE, resolved: true},
		{name: "stale", state: unix.NUD_STALE, resolved: true},
		{name: "delay", state: unix.NUD_DELAY, resolved: true},
		{name: "probe", state: unix.NUD_PROBE, resolved: true},
		{name: "permanent", state: unix.NUD_PERMANENT, resolved: true},
		{name: "noarp", state: unix.NUD_NOARP, resolved: true},
		{name: "incomplete", state: unix.NUD_INCOMPLETE, resolved: false},
		{name: "failed", state: unix.NUD_FAILED, resolved: false},
		{name: "none", state: unix.NUD_NONE, resolved: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := neighStateResolved(testCase.state); got != testCase.resolved {
				t.Fatalf("neighStateResolved(%#x) = %v, want %v",
					testCase.state, got, testCase.resolved)
			}
		})
	}
}
