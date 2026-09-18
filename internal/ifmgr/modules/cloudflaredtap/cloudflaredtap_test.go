package cloudflaredtap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/notify"
)

func TestNew_DefaultsApplied(t *testing.T) {
	m, err := New(Config{Unit: "cloudflared-oob"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mod, ok := m.(*Module)
	if !ok {
		t.Fatalf("New returned wrong type")
	}
	if mod.cfg.Unit != "cloudflared-oob" {
		t.Errorf("unit=%q want cloudflared-oob", mod.cfg.Unit)
	}
	if mod.cfg.JournalctlPath != "" {
		t.Errorf("JournalctlPath should be empty until Init applies the default; got %q",
			mod.cfg.JournalctlPath)
	}
	if len(mod.cfg.DowngradePatterns) != 0 {
		t.Errorf("DowngradePatterns should be empty; got %d", len(mod.cfg.DowngradePatterns))
	}
}

func TestNew_DowngradePatternsParsed(t *testing.T) {
	m, _ := New(Config{
		Unit:              "x",
		DowngradePatterns: []string{"foo", "bar.*"},
	})
	mod := m.(*Module)
	if len(mod.cfg.DowngradePatterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d", len(mod.cfg.DowngradePatterns))
	}
	if mod.cfg.DowngradePatterns[0] != "foo" || mod.cfg.DowngradePatterns[1] != "bar.*" {
		t.Errorf("patterns: %v", mod.cfg.DowngradePatterns)
	}
}

func TestNew_JournalctlPathOverride(t *testing.T) {
	m, _ := New(Config{Unit: "x", JournalctlPath: "/usr/bin/journalctl-test"})
	if m.(*Module).cfg.JournalctlPath != "/usr/bin/journalctl-test" {
		t.Errorf("journalctl_path not parsed")
	}
}

func TestMapPriority(t *testing.T) {
	cases := []struct {
		p    int
		want slog.Level
	}{
		{0, slog.LevelError}, // emerg
		{1, slog.LevelError}, // alert
		{2, slog.LevelError}, // crit
		{3, slog.LevelError}, // err
		{4, slog.LevelWarn},  // warning
		{5, slog.LevelInfo},  // notice
		{6, slog.LevelInfo},  // info
		{7, slog.LevelDebug}, // debug
		{99, slog.LevelInfo}, // out of range fallback
	}
	for _, c := range cases {
		got := mapPriority(c.p)
		if got != c.want {
			t.Errorf("mapPriority(%d) = %v want %v", c.p, got, c.want)
		}
	}
}

func TestMatchAny(t *testing.T) {
	pats := []*regexp.Regexp{
		regexp.MustCompile(`failed to dial to edge with quic: timeout`),
		regexp.MustCompile(`datagram manager encountered a failure while serving`),
	}
	if !matchAny(pats, "WRN failed to dial to edge with quic: timeout connIndex=2") {
		t.Errorf("expected match for noisy edge-rotation line")
	}
	if !matchAny(pats, "datagram manager encountered a failure while serving connIndex=0") {
		t.Errorf("expected match for datagram-manager line")
	}
	if matchAny(pats, "Registered tunnel connection connIndex=0 location=sjc07") {
		t.Errorf("unexpected match for benign Registered line")
	}
	if matchAny(nil, "anything") {
		t.Errorf("nil patterns should never match")
	}
}

func TestParsePriority(t *testing.T) {
	if got := parsePriority("6"); got != 6 {
		t.Errorf("parsePriority(6) = %d", got)
	}
	if got := parsePriority("not-a-number"); got != 0 {
		t.Errorf("non-numeric should return 0; got %d", got)
	}
	if got := parsePriority(""); got != 0 {
		t.Errorf("empty should return 0; got %d", got)
	}
}

func TestInitReturnsDisabledSentinelWhenUnitEmpty(t *testing.T) {
	t.Parallel()

	module, err := New(Config{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	env := &ifmgr.Env{
		Iface: "lo",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(
			&config.Config{},
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			"mwan-ifmgr",
		)),
	}
	initErr := module.Init(context.Background(), env)
	if initErr == nil {
		t.Fatal("Init returned nil error for empty Unit, want ErrModuleDisabled")
	}
	if !errors.Is(initErr, ifmgr.ErrModuleDisabled) {
		t.Fatalf("Init returned err=%v, want errors.Is(err, ifmgr.ErrModuleDisabled)", initErr)
	}
}
