package config_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/notify"
)

const (
	globalKind   = "wg-reconcile-failed"
	overrideKind = "wg-peer-stalled"
)

type emitCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func (c *emitCounter) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (c *emitCounter) Handle(_ context.Context, record slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[record.Message]++
	return nil
}

func (c *emitCounter) WithAttrs(_ []slog.Attr) slog.Handler { return c }
func (c *emitCounter) WithGroup(_ string) slog.Handler      { return c }

func (c *emitCounter) count(message string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.counts[message]
}

func TestNotifyConfigRepeatCadence(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{
			name: "notify section",
			text: "[notify]\nrepeat_every = \"30m\"\n\n[notify.per_kind]\nwg-peer-stalled = \"2h\"\n",
		},
		{
			name: "ifmgr alerts fallback",
			text: "[ifmgr.alerts]\nrepeat_every = \"30m\"\n\n[ifmgr.alerts.per_module]\nwg-peer-stalled = \"2h\"\n",
		},
		{
			name: "notify section over ifmgr alerts",
			text: "[notify]\nrepeat_every = \"30m\"\n\n[notify.per_kind]\nwg-peer-stalled = \"2h\"\n\n" +
				"[ifmgr.alerts]\nrepeat_every = \"1m\"\n\n[ifmgr.alerts.per_module]\nwg-peer-stalled = \"1m\"\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(configPath, []byte(tc.text), 0o600); err != nil {
				t.Fatalf("write config: %v", err)
			}
			t.Setenv("MWAN_CONFIG", configPath)

			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			counter := &emitCounter{mu: sync.Mutex{}, counts: map[string]int{}}
			manager, err := notify.New(notify.EmailConfig{}, cfg.NotifyConfig(), slog.New(counter), "mwan-test")
			if err != nil {
				t.Fatalf("create notifier: %v", err)
			}

			start := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
			send := func(kind string, offset time.Duration) {
				manager.Notify(context.Background(), notify.Event{
					Now:        start.Add(offset),
					Level:      slog.LevelWarn,
					Kind:       kind,
					Key:        "peer1",
					Message:    kind,
					Fields:     nil,
					IsRecovery: false,
				})
			}

			send(globalKind, 0)
			send(globalKind, 29*time.Minute)
			if got := counter.count(globalKind); got != 1 {
				t.Fatalf("global log count before 30m: %d", got)
			}
			send(globalKind, 30*time.Minute)
			if got := counter.count(globalKind); got != 2 {
				t.Fatalf("global log count at 30m: %d", got)
			}

			send(overrideKind, 0)
			send(overrideKind, 30*time.Minute)
			send(overrideKind, 119*time.Minute)
			if got := counter.count(overrideKind); got != 1 {
				t.Fatalf("override log count before 2h: %d", got)
			}
			send(overrideKind, 2*time.Hour)
			if got := counter.count(overrideKind); got != 2 {
				t.Fatalf("override log count at 2h: %d", got)
			}
		})
	}
}
