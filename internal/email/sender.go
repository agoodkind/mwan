package email

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/send-email/mailer"
)

// Sender sends email with automatic fallback: try the default route first,
// then retry via the OOB bind interface if the first attempt fails.
type Sender struct {
	apiKey    string
	from      string
	caller    string
	bindIface string
	log       *slog.Logger
}

// NewSender creates a sender that tries the default route first, then falls
// back to bindIface (OOB) on failure. If bindIface is empty, no fallback.
func NewSender(smtp2goAPIKey, from, bindIface, caller string, log *slog.Logger) *Sender {
	return &Sender{
		apiKey:    smtp2goAPIKey,
		from:      from,
		caller:    caller,
		bindIface: bindIface,
		log:       log,
	}
}

func (s *Sender) Send(ctx context.Context, to, subject, body string) error {
	if s.apiKey == "" {
		return nil
	}

	msg := mailer.Message{
		To:      to,
		From:    s.from,
		Subject: subject,
		Body:    body,
		Caller:  s.caller,
	}

	// Try default route first
	err := s.sendVia(ctx, "", msg)
	if err == nil {
		return nil
	}

	// If no OOB interface configured, return the primary error
	if s.bindIface == "" {
		return err
	}

	// Fallback to OOB interface
	if s.log != nil {
		s.log.WarnContext(ctx, "email failed via default route, retrying via OOB",
			"bind_iface", s.bindIface, "primary_err", err)
	}
	return s.sendVia(ctx, s.bindIface, msg)
}

func (s *Sender) sendVia(ctx context.Context, bindIface string, msg mailer.Message) error {
	transport := mailer.MethodAuto
	if bindIface != "" {
		transport = mailer.MethodHTTP
	}
	cfg := mailer.Config{
		SMTP2GOAPIKey:     s.apiKey,
		DefaultFromDomain: "goodkind.io",
		Transport:         transport,
		BindInterface:     bindIface,
	}
	m := mailer.New(cfg)
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
