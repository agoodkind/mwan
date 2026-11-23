// Package email sends gateway alert messages through the configured transport.
package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/send-email/mailer"
)

// Sender sends alert email through the configured transport.
type Sender struct {
	cfg SenderConfig
}

// SenderConfig supplies mail transport settings and sender identity.
type SenderConfig struct {
	Mail           mailer.Config
	From, Caller   string
	AttemptTimeout time.Duration
}

// NewSenderFromConfig validates the transport settings and creates a sender.
func NewSenderFromConfig(cfg SenderConfig) (*Sender, error) {
	if cfg.Mail.SMTP2GOAPIKey == "" && cfg.Mail.MsmtprcPath == "" {
		return nil, errors.New("configure SMTP2GO credentials or msmtprc_path for email delivery")
	}

	if cfg.AttemptTimeout <= 0 {
		return nil, errors.New("email attempt timeout must be positive")
	}

	return &Sender{cfg: cfg}, nil
}

// Send delivers a plain-text message to the requested recipient.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	if s.cfg.Mail.SMTP2GOAPIKey == "" && s.cfg.Mail.MsmtprcPath == "" {
		return errors.New("configure SMTP2GO credentials or msmtprc_path for email delivery")
	}

	if to == "" {
		return errors.New("email recipient is required")
	}

	msg := mailer.Message{
		To:      to,
		From:    s.cfg.From,
		Subject: subject,
		Body:    body,
		Caller:  s.cfg.Caller,
	}

	cfg := s.cfg.Mail
	cfg.BindInterface = ""
	if cfg.MsmtprcPath != "" {
		cfg.Transport = mailer.MethodSendmail
	}

	m := mailer.New(cfg)
	ctx, cancel := context.WithTimeout(ctx, s.cfg.AttemptTimeout)
	defer cancel()
	err := m.Send(ctx, msg)
	if err != nil {
		slog.WarnContext(ctx, "mwan.email.send_attempt_failed", slog.String("err", err.Error()))

		return fmt.Errorf("send email: %w", err)
	}

	return nil
}
