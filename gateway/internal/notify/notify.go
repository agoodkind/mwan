// Package notify is the single boundary every mwan email exits through.
// It wraps the per-(kind, key) state-change semantics carved out of
// internal/ifmgr (state map plus repeat cadence) with a typed Event
// shape, and renders the email body via BuildEmailBody.
// Constructors accept EmailConfig and Config without depending on
// internal/config. An empty EmailConfig produces a Manager that writes
// alerts only to the logger.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"goodkind.io/gklog"
	"goodkind.io/mwan/internal/email"
)

// EmailConfig uses compatible TOML tags so configuration loaders can
// decode existing [email] sections directly into notification settings.
type EmailConfig struct {
	SMTP2GOAPIKey string `toml:"smtp2go_api_key"`
	AlertEmail    string `toml:"alert_email"`
	From          string `toml:"from"`
	SubjectPrefix string `toml:"subject_prefix"`
	BindIface     string `toml:"bind_iface"`
	MinLevel      string `toml:"min_level"`
	Cooldown      string `toml:"cooldown"`
}

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

// New returns a Manager that writes alerts only to the logger when
// SMTP2GOAPIKey or AlertEmail is empty.
//
// serviceName is the X-Mailer-style identifier on outgoing messages,
// typically the systemd unit name (e.g. "mwan-watchdog", "mwan-agent").
func New(emailCfg EmailConfig, cfg Config, log *slog.Logger, serviceName string) (*Manager, error) {
	var sink Sink
	if emailCfg.SMTP2GOAPIKey != "" && emailCfg.AlertEmail != "" {
		bootLogger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
		sender := email.NewSender(
			emailCfg.SMTP2GOAPIKey,
			emailCfg.From,
			emailCfg.BindIface,
			serviceName,
			bootLogger,
		)
		sink = newEmailSink(sender, emailCfg.AlertEmail, emailCfg.SubjectPrefix, parseMinLevel(emailCfg.MinLevel))
	}
	m, err := newManager(log, cfg, sink)
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
func FromConfig(emailCfg EmailConfig, cfg Config, log *slog.Logger, serviceName string) Notifier {
	if log == nil {
		return NullNotifier{}
	}
	m, err := New(emailCfg, cfg, log, serviceName)
	if err != nil {
		log.Error("notify: construction failed, falling back to null notifier", "err", err)
		return NullNotifier{}
	}
	return m
}

// parseMinLevel mirrors gklog.ParseLevel. Re-exported here so the email
// sink stays decoupled from internal/logging.
func parseMinLevel(s string) slog.Level { return gklog.ParseLevel(s) }
