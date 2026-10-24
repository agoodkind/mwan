// Package email sends alert mail through SMTP2GO, retrying over the
// out-of-band interface when the default route fails. An alert about a
// connectivity failure has to survive that same failure, so the fallback is
// the reason this package exists rather than calling the mailer directly.
package email

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/send-email/mailer"
)

// Sender sends email with automatic fallback: try the default route first,
// then retry via the OOB bind interface if the first attempt fails.
type Sender struct {
	cfg SenderConfig
	log *slog.Logger
}

type SenderConfig struct {
	Mail           mailer.Config
	From, Caller   string
	AttemptTimeout time.Duration
}

func NewSenderFromConfig(cfg SenderConfig, log *slog.Logger) (*Sender, error) {
	if cfg.Mail.SMTP2GOAPIKey == "" && cfg.Mail.MsmtprcPath == "" {
		return nil, errors.New("configure SMTP2GO credentials or msmtprc_path for email delivery")
	}
	if cfg.AttemptTimeout <= 0 {
		return nil, errors.New("email attempt timeout must be positive")
	}
	return &Sender{cfg: cfg, log: log}, nil
}

// NewSender creates a sender that tries the default route first, then falls
// back to bindIface (OOB) on failure. If bindIface is empty, no fallback.
func NewSender(smtp2goAPIKey, from, bindIface, caller string, log *slog.Logger) *Sender {
	return &Sender{cfg: SenderConfig{
		Mail: mailer.Config{SMTP2GOAPIKey: smtp2goAPIKey, BindInterface: bindIface, DefaultFromDomain: "goodkind.io", Transport: mailer.MethodAuto},
		From: from, Caller: caller, AttemptTimeout: 15 * time.Second,
	}, log: log}
}

// Send delivers one message, trying the default route and then the OOB
// interface. It reports success without sending when no API key is configured,
// so a host with no mail credentials runs the alert path without failing it.
// With no OOB interface configured it returns the first attempt's error.
func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	if s.cfg.Mail.SMTP2GOAPIKey == "" && s.cfg.Mail.MsmtprcPath == "" {
		return nil
	}

	msg := mailer.Message{
		To:      to,
		From:    s.cfg.From,
		Subject: subject,
		Body:    body,
		Caller:  s.cfg.Caller,
	}
	return s.SendMessage(ctx, msg)
}

func (s *Sender) SendMessage(ctx context.Context, msg mailer.Message) error {
	if s.cfg.Mail.SMTP2GOAPIKey == "" && s.cfg.Mail.MsmtprcPath == "" {
		return errors.New("configure SMTP2GO credentials or msmtprc_path for email delivery")
	}
	if msg.To == "" {
		return errors.New("email recipient is required")
	}
	if msg.From == "" {
		msg.From = s.cfg.From
	}
	if msg.Caller == "" {
		msg.Caller = s.cfg.Caller
	}

	// Try default route first
	err := s.sendVia(ctx, "", msg)
	if err == nil {
		return nil
	}

	// If no OOB interface configured, return the primary error
	if s.cfg.Mail.BindInterface == "" || s.cfg.Mail.MsmtprcPath != "" {
		return err
	}

	// Fallback to OOB interface
	if s.log != nil {
		s.log.WarnContext(ctx, "email failed via default route, retrying via OOB",
			"bind_iface", s.cfg.Mail.BindInterface, "primary_err", err)
	}
	return s.sendVia(ctx, s.cfg.Mail.BindInterface, msg)
}

func (s *Sender) sendVia(ctx context.Context, bindIface string, msg mailer.Message) error {
	cfg := s.cfg.Mail
	cfg.BindInterface = bindIface
	if cfg.MsmtprcPath != "" {
		cfg.Transport = mailer.MethodSendmail
	}
	if bindIface != "" {
		cfg.Transport = mailer.MethodHTTP
	}
	m := mailer.New(cfg)
	ctx, cancel := context.WithTimeout(ctx, s.cfg.AttemptTimeout)
	defer cancel()
	err := m.Send(ctx, msg)
	if err != nil {
		slog.WarnContext(ctx, "mwan.email.send_attempt_failed",
			slog.String("bind_iface", bindIface),
			slog.String("err", err.Error()))
		return fmt.Errorf("send email via %q: %w", bindIface, err)
	}
	return nil
}
