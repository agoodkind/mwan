package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/mwan/internal/email"
	"goodkind.io/send-email/mailer"
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

// Delivery uses the original severity even when the subject uses a status label.
func (s *emailSink) Send(ctx context.Context, level slog.Level, msg string, fields []slog.Attr) error {
	if level < s.minLevel {
		return nil
	}
	resolved := false
	notice := false
	for _, field := range fields {
		if field.Key == "resolved" {
			resolved = field.Value.Bool()
		}
		if field.Key == "notice" {
			notice = field.Value.Bool()
		}
	}
	subject := buildSubject(s.subjectPrefix, level, msg, resolved, notice)
	if resolved {
		msg = "Recovered: " + strings.TrimSpace(strings.TrimPrefix(msg, "RECOVERED:"))
	} else if notice {
		msg = "Notice: " + msg
	}
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

func (s *emailSink) SendMessage(ctx context.Context, message mailer.Message) error {
	if message.To == "" {
		message.To = s.to
	}
	if s.subjectPrefix != "" && !strings.HasPrefix(message.Subject, s.subjectPrefix) {
		message.Subject = s.subjectPrefix + " " + message.Subject
	}
	if err := s.sender.SendMessage(ctx, message); err != nil {
		slog.WarnContext(ctx, "notify message delivery failed", "error", err, "to", message.To)
		return fmt.Errorf("send email message: %w", err)
	}
	return nil
}

func buildSubject(prefix string, level slog.Level, msg string, resolved, notice bool) string {
	lvl := strings.ToUpper(level.String())
	if notice {
		lvl = "Notice"
	}
	if resolved {
		lvl = "Recovered"
		msg = strings.TrimSpace(strings.TrimPrefix(msg, "RECOVERED:"))
	}
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return lvl + ": " + msg
	}
	return prefix + " " + lvl + ": " + msg
}
