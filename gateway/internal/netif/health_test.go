package netif

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadHealthState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mwan-health.state")
	content := []byte("att:healthy\nwebpass:unhealthy\nmonkeybrains:healthy\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write state file: %v", err)
	}

	states, err := ReadHealthState(path)
	if err != nil {
		t.Fatalf("ReadHealthState: %v", err)
	}

	cases := []struct {
		name string
		wan  string
		want string
	}{
		{name: "healthy", wan: "att", want: HealthStateHealthy},
		{name: "unhealthy", wan: "webpass", want: HealthStateUnhealthy},
		{name: "missing key", wan: "missing", want: HealthStateUnknown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := states.State(tc.wan)
			if got != tc.want {
				t.Fatalf("state got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadHealthStateMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.state")
	states, err := ReadHealthState(path)
	if err != nil {
		t.Fatalf("ReadHealthState: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("missing file states got %#v, want empty", states)
	}
}

func TestHealthIsHealthy(t *testing.T) {
	cases := []struct {
		name  string
		state string
		want  bool
	}{
		{name: "healthy", state: HealthStateHealthy, want: true},
		{name: "unknown", state: HealthStateUnknown, want: true},
		{name: "empty", state: "", want: true},
		{name: "unhealthy", state: HealthStateUnhealthy, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := HealthIsHealthy(tc.state)
			if got != tc.want {
				t.Fatalf("HealthIsHealthy(%q) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}
