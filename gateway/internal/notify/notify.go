// Package notify tracks active alerts and sends log messages and email.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/gklog"
	"goodkind.io/mwan/internal/email"
	"goodkind.io/send-email/mailer"
)

// EmailConfig contains the recipient and mail transport settings for alerts.
type EmailConfig struct {
	SMTP2GOAPIKey string `toml:"smtp2go_api_key"`
	AlertEmail    string `toml:"alert_email"`
	From          string `toml:"from"`
	SubjectPrefix string `toml:"subject_prefix"`
	MinLevel      string `toml:"min_level"`
	Cooldown      string `toml:"cooldown"`
	MsmtprcPath   string `toml:"msmtprc_path"`
}

// Event records an alert or recovery for a keyed gateway resource.
type Event struct {
	Now        time.Time
	Level      slog.Level
	Kind       string
	Key        string
	Message    string
	Fields     []slog.Attr
	IsRecovery bool
}

// Notifier tracks active alert state and sends configured notifications.
type Notifier interface {
	Notify(ctx context.Context, ev Event)
	Resolve(ctx context.Context, kind, key, msg string, fields ...slog.Attr)
	Active(kind, key string) bool
}

// Config controls reminder intervals for all alerts and specific alert kinds.
// A zero repeat interval disables reminders.
type Config struct {
	RepeatEvery time.Duration
	PerKind     map[string]time.Duration
}

// New configures and returns an alert manager. Email delivery starts when a
// recipient and mail transport credentials exist. The email footer identifies
// the service.
func New(emailCfg EmailConfig, cfg Config, log *slog.Logger, serviceName string) (*Manager, error) {
	var sink Sink
	var sender *email.Sender
	if (emailCfg.SMTP2GOAPIKey != "" || emailCfg.MsmtprcPath != "") && emailCfg.AlertEmail != "" {
		var err error
		sender, err = email.NewSenderFromConfig(email.SenderConfig{
			Mail: mailer.Config{
				SMTP2GOAPIKey: emailCfg.SMTP2GOAPIKey,
				MsmtprcPath:   emailCfg.MsmtprcPath,
				Transport:     mailer.MethodAuto,
			},
			From:           emailCfg.From,
			Caller:         serviceName,
			AttemptTimeout: 15 * time.Second,
		})
		if err != nil {
			return nil, fmt.Errorf("notify email: %w", err)
		}

		sink = newEmailSink(
			sender,
			emailCfg.AlertEmail,
			emailCfg.SubjectPrefix,
			parseMinLevel(emailCfg.MinLevel),
		)
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

// FromConfig disables notifications without a logger or after initialization
// fails.
// It logs initialization errors before returning the null notifier.
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

func parseMinLevel(s string) slog.Level { return gklog.ParseLevel(s) }
