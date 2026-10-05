// Package notify is the single boundary every mwan email exits through.
// It wraps the per-(kind, key) state-change semantics carved out of
// internal/ifmgr (state map plus repeat cadence) with a typed Event
// shape, and renders the email body via BuildEmailBody. Constructors
// accept the parsed *config.Config and return either a Manager wired to
// an email Sink or a NullNotifier when [email] is unconfigured.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"goodkind.io/gklog"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/email"
)

// Event is the structured payload one Notify call carries through the
// boundary. Kind is required and identifies the alert type; Key is the
// optional sub-key (e.g. peer pubkey, vmid, iface). Now is supplied by
// the caller so tests can drive a deterministic clock. IsRecovery flips
// the Manager into the Resolve path; callers normally use Notify or
// Resolve directly rather than building Event by hand.
type Event struct {
	Now        time.Time
	Level      slog.Level
	Kind       string
	Key        string
	Message    string
	Fields     []slog.Attr
	IsRecovery bool
}

// Notifier is the interface every mwan caller depends on. Manager and
// NullNotifier both satisfy it. Notify and Resolve own the state-change
// suppression; Active reports whether an alert is currently in the
// "fired but not resolved" state for callers that want to escalate.
type Notifier interface {
	Notify(ctx context.Context, ev Event)
	Resolve(ctx context.Context, kind, key, msg string, fields ...slog.Attr)
	Active(kind, key string) bool
}

// Config controls the Manager's repeat cadence. RepeatEvery is the
// global default applied to every (kind, key) unless overridden in
// PerKind. Zero RepeatEvery means alerts fire once per transition and
// never repeat. PerKind is keyed by alert kind (the Kind field on
// Event), not by module name, so one module emitting multiple kinds
// gets a separate cadence per kind.
type Config struct {
	RepeatEvery time.Duration
	PerKind     map[string]time.Duration
}

// New constructs a Manager wired to an email Sink built from cfg.Email.
// log must be non-nil; it is used for journald emit. When cfg.Email is
// unconfigured (no SMTP2GOAPIKey or AlertEmail), New returns a Manager
// whose sink is the journal-only path so alerts still surface in logs.
//
// serviceName is the X-Mailer-style identifier on outgoing messages,
// typically the systemd unit name (e.g. "mwan-watchdog", "mwan-agent").
func New(cfg *config.Config, log *slog.Logger, serviceName string) (*Manager, error) {
	mcfg := configFromEmail(cfg)
	var sink Sink
	if cfg != nil && cfg.Email.SMTP2GOAPIKey != "" && cfg.Email.AlertEmail != "" {
		bootLogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
		sender := email.NewSender(
			cfg.Email.SMTP2GOAPIKey,
			cfg.Email.From,
			cfg.Email.BindIface,
			serviceName,
			bootLogger,
		)
		sink = newEmailSink(sender, cfg.Email.AlertEmail, cfg.Email.SubjectPrefix, parseMinLevel(cfg.Email.MinLevel))
	}
	m, err := newManager(log, mcfg, sink)
	if err != nil {
		if log != nil {
			log.Error("notify: construct manager failed", "err", err, "service", serviceName)
		}
		return nil, fmt.Errorf("notify new: %w", err)
	}
	return m, nil
}

// FromConfig is the constructor most call sites use. It mirrors the
// old logging.EmailFromConfig shape: returns a Notifier that satisfies
// the interface, falling back to NullNotifier when [email] is
// unconfigured. Construction errors collapse to NullNotifier so the
// caller never crashes on a missing alert path; the underlying error
// is logged at boot time through the supplied logger.
func FromConfig(cfg *config.Config, log *slog.Logger, serviceName string) Notifier {
	if cfg == nil || log == nil {
		return NullNotifier{}
	}
	m, err := New(cfg, log, serviceName)
	if err != nil {
		log.Error("notify: construction failed, falling back to null notifier", "err", err)
		return NullNotifier{}
	}
	return m
}

// configFromEmail extracts the repeat cadence settings. cfg.Notify is
// the preferred source; cfg.IfMgr.Alerts is read as a fallback so
// callers wired to the ifmgr AlertManager keep working until slice B
// migrates them. When both are populated, cfg.Notify wins because the
// notify section is the new single source of truth.
func configFromEmail(cfg *config.Config) Config {
	if cfg == nil {
		return Config{RepeatEvery: 0, PerKind: nil}
	}
	repeat, perKind := readRepeatCadence(cfg)
	return Config{RepeatEvery: repeat, PerKind: perKind}
}

// readRepeatCadence resolves RepeatEvery and PerKind from the
// preferred [notify] section first, falling back to [ifmgr.alerts].
// Invalid duration strings are silently dropped on the assumption that
// the validator path catches them earlier.
func readRepeatCadence(cfg *config.Config) (time.Duration, map[string]time.Duration) {
	var repeat time.Duration
	if cfg.Notify.RepeatEvery != "" {
		if d, err := time.ParseDuration(cfg.Notify.RepeatEvery); err == nil {
			repeat = d
		}
	} else if cfg.IfMgr.Alerts.RepeatEvery != "" {
		if d, err := time.ParseDuration(cfg.IfMgr.Alerts.RepeatEvery); err == nil {
			repeat = d
		}
	}
	source := cfg.Notify.PerKind
	if len(source) == 0 {
		source = cfg.IfMgr.Alerts.PerModule
	}
	if len(source) == 0 {
		return repeat, nil
	}
	perKind := make(map[string]time.Duration, len(source))
	for kind, raw := range source {
		if raw == "" {
			continue
		}
		if d, err := time.ParseDuration(raw); err == nil {
			perKind[kind] = d
		}
	}
	return repeat, perKind
}

// parseMinLevel mirrors gklog.ParseLevel. Re-exported here so the email
// sink stays decoupled from internal/logging.
func parseMinLevel(s string) slog.Level { return gklog.ParseLevel(s) }
