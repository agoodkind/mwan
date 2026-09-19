package netif

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyToPath(t *testing.T) {
	cases := []struct {
		key  string
		want string
	}{
		{
			key:  "net.ipv6.conf.eth0.disable_ipv6",
			want: "/proc/sys/net/ipv6/conf/eth0/disable_ipv6",
		},
		{
			key:  "net.ipv6.conf.eth0.accept_ra",
			want: "/proc/sys/net/ipv6/conf/eth0/accept_ra",
		},
		{
			key:  "net.ipv4.conf.all.forwarding",
			want: "/proc/sys/net/ipv4/conf/all/forwarding",
		},
		{
			// VLAN-style NIC name.
			key:  "net.ipv6.conf.enatt0.3242.accept_ra",
			want: "/proc/sys/net/ipv6/conf/enatt0.3242/accept_ra",
		},
		{
			// VLAN-style NIC name.
			key:  "net.ipv6.conf.enatt0.3242.disable_ipv6",
			want: "/proc/sys/net/ipv6/conf/enatt0.3242/disable_ipv6",
		},
		{
			// Non-conf path: every dot becomes a slash.
			key:  "net.core.somaxconn",
			want: "/proc/sys/net/core/somaxconn",
		},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := keyToPath(tc.key); got != tc.want {
				t.Errorf("keyToPath(%q) = %q, want %q", tc.key, got, tc.want)
			}
		})
	}
}

// TestProcSysctlRunnerUnderRootReadsAndWritesBelowIt proves the runner
// resolves a key below its root rather than at the machine's own /proc/sys,
// which is what lets the install verb's apply step be exercised against a
// directory tree without touching the kernel running the test.
func TestProcSysctlRunnerUnderRootReadsAndWritesBelowIt(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	// A VLAN link, whose name carries a dot the key mapping must keep.
	dir := filepath.Join(root, "proc", "sys", "net", "ipv4", "conf", "enatt0.3242")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	path := filepath.Join(dir, "rp_filter")
	if err := os.WriteFile(path, []byte("2\n"), 0o600); err != nil {
		t.Fatalf("seed %s: %v", path, err)
	}
	runner := NewProcSysctlRunnerUnder(slog.New(slog.DiscardHandler), false, root)

	value, err := runner.Get(t.Context(), "net.ipv4.conf.enatt0.3242.rp_filter")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if value != "2" {
		t.Fatalf("Get = %q, want 2", value)
	}
	if err := runner.Set(t.Context(), "net.ipv4.conf.enatt0.3242.rp_filter", "0"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if strings.TrimRight(string(written), "\n") != "0" {
		t.Fatalf("%s = %q, want 0", path, written)
	}
}

// TestProcSysctlRunnerWithNoRootUsesTheHostPath proves the daemon's runner is
// unchanged by the root support: with no root it still resolves to the
// machine's own /proc/sys, so a missing key fails rather than being created
// somewhere else.
func TestProcSysctlRunnerWithNoRootUsesTheHostPath(t *testing.T) {
	t.Parallel()
	runner := NewProcSysctlRunner(slog.New(slog.DiscardHandler), false)

	if got := runner.path("net.ipv4.conf.all.rp_filter"); got != "/proc/sys/net/ipv4/conf/all/rp_filter" {
		t.Fatalf("path = %q, want the host path", got)
	}
}
