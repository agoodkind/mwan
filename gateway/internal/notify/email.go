package notify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"goodkind.io/mwan/internal/email"
)

type emailSink struct {
	sender        *email.Sender
	to            string
	subjectPrefix string
	minLevel      slog.Level
	clock         clock
}

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

func (s *emailSink) Send(
	ctx context.Context,
	level slog.Level,
	msg string,
	fields []slog.Attr,
) error {
	if level < s.minLevel {
		return nil
	}

	subject := buildSubject(s.subjectPrefix, level, msg)
	r := slog.NewRecord(s.clock.Now(), level, msg, 0)
	r.AddAttrs(fields...)
	body := BuildEmailBody(r, nil)
	err := s.sender.Send(ctx, s.to, subject, body)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"notify email send failed",
			"err",
			err,
			"to",
			s.to,
			"alert_level",
			level.String(),
			"alert_msg",
			msg,
		)

		return fmt.Errorf("notify email send: %w", err)
	}

	return nil
}

func buildSubject(prefix string, level slog.Level, msg string) string {
	lvl := strings.ToUpper(level.String())
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return lvl + ": " + msg
	}

	return prefix + " " + lvl + ": " + msg
}
