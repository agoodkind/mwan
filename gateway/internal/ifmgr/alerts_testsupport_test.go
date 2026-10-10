package ifmgr

import (
	"log/slog"
	"time"

	"goodkind.io/mwan/internal/notify"
)

// AlertConfig is the test-only entry point for constructing an AlertManager
// with a repeat cadence.
type AlertConfig struct {
	RepeatEvery time.Duration
}

// NewAlertManager builds an AlertManager wired to notify.FromConfig for
// package-local tests.
func NewAlertManager(log *slog.Logger, cfg AlertConfig) *AlertManager {
	if log == nil {
		log = slog.Default()
	}
	notifyCfg := notify.Config{RepeatEvery: 0, PerKind: nil}
	if cfg.RepeatEvery > 0 {
		notifyCfg.RepeatEvery = cfg.RepeatEvery
	}
	return WrapNotifier(notify.FromConfig(notify.EmailConfig{}, notifyCfg, log, "mwan-ifmgr"))
}
