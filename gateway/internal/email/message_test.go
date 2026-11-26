package email_test

import (
	"errors"
	"net/textproto"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/internal/email"
	"goodkind.io/mwan/gateway/internal/testsmtp"
	"goodkind.io/send-email/mailer"
)

func TestSendReturnsSMTPRejection(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	sender, err := email.NewSenderFromConfig(email.SenderConfig{
		Mail:           mailer.Config{MsmtprcPath: server.Path},
		AttemptTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	server.Reject(true)
	err = sender.Send(t.Context(), "recipient@example.com", "WAN probe failed", "Probe timed out")
	const temporaryDeliveryFailure = 451
	if reply, ok := errors.AsType[*textproto.Error](err); !ok ||
		reply.Code != temporaryDeliveryFailure {
		t.Fatalf("expected a temporary SMTP delivery failure, got %v", err)
	}
}
