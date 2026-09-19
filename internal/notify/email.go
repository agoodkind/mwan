package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/mwan/internal/email"
)

// emailSink delivers each rendered alert to a single recipient via the
// email Sender. It applies the configured subject prefix, drops emits
// below minLevel, and renders the body via BuildEmailBody so the alert
// reads cleanly above the host-snapshot footer the send-email path
// appends downstream.
type emailSink struct {
	sender        *email.Sender
	to            string
	subjectPrefix string
	minLevel      slog.Level
	clock         clock
}

// newEmailSink constructs the sink the Manager hands records to.
// Passing a nil sender from any caller is treated as "no email
// delivery"; the Manager handles the nil-Sink path by logging via
// journal only.
func newEmailSink(sender *email.Sender, to, subjectPrefix string, minLevel slog.Level) Sink {
	if sender == nil || strings.TrimSpace(to) == "" {
		return nil
	}
	return &emailSink{
		sender:        sender,
		to:            to,
		subjectPrefix: subjectPrefix,
		minLevel:      minLevel,
		clock:         realClock{},
	}
}

// Send delivers one record. Records below minLevel are dropped so a
// daemon configured with min_level=ERROR does not ship every WARN to
// the inbox. The subject is composed as "[prefix] LEVEL: message" or
// "LEVEL: message" when no prefix is configured. The function name
// matches the [io.Writer]-style Send shape so the wrapped-error rule
// recognises it as a transport boundary.
func (s *emailSink) Send(ctx context.Context, level slog.Level, msg string, fields []slog.Attr) error {
	if level < s.minLevel {
		return nil
	}
	subject := buildSubject(s.subjectPrefix, level, msg)
	r := slog.NewRecord(s.clock.Now(), level, msg, 0)
	r.AddAttrs(fields...)
	body := BuildEmailBody(r, nil)
	err := s.sender.Send(ctx, s.to, subject, body)
	if err != nil {
		slog.ErrorContext(ctx, "notify email send failed",
			"err", err, "to", s.to, "alert_level", level.String(), "alert_msg", msg)
		return fmt.Errorf("notify email send: %w", err)
	}
	return nil
}

// buildSubject composes the email subject line as "[prefix] LEVEL:
// message" or "LEVEL: message" if prefix is empty. The level token
// matches the upper-case [slog.Level.String] output so subjects sort
// the same way the original logging email_handler subjects did.
func buildSubject(prefix string, level slog.Level, msg string) string {
	lvl := strings.ToUpper(level.String())
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return lvl + ": " + msg
	}
	return prefix + " " + lvl + ": " + msg
}
