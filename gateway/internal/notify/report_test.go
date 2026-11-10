package notify_test

import (
	"strings"
	"testing"

	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/testsmtp"
	"goodkind.io/send-email/mailer"
)

func TestSendReportReturnsAcceptance(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	logger, closer := logging.New(logging.Config{BuildVersion: "test"})
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Error(err)
		}
	})
	manager, err := notify.New(notify.EmailConfig{MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", SubjectPrefix: "[lab]", MinLevel: "error"}, notify.Config{}, logger, "mwan-report")
	if err != nil {
		t.Fatal(err)
	}
	message := mailer.Message{Subject: "Daily report", Body: "Daily measurements"}
	if err := manager.SendReport(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	accepted := string(server.Next(t))
	if !strings.Contains(accepted, "Subject: [lab] Daily report") || !strings.Contains(accepted, "recipient@example.com") {
		t.Fatal("report lost configured subject prefix or recipient")
	}
	server.Reject(true)
	if err := manager.SendReport(t.Context(), message); err == nil {
		t.Fatal("report did not return SMTP rejection")
	}
}

func TestSendReportRejectsMissingTransport(t *testing.T) {
	t.Parallel()
	logger, closer := logging.New(logging.Config{BuildVersion: "test"})
	t.Cleanup(func() {
		if err := closer.Close(); err != nil {
			t.Error(err)
		}
	})
	manager, err := notify.New(notify.EmailConfig{}, notify.Config{}, logger, "mwan-report")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SendReport(t.Context(), mailer.Message{Subject: "Report", Body: "Measurements"}); err == nil || !strings.Contains(err.Error(), "configure email delivery and alert_email") {
		t.Fatalf("unconfigured report delivery did not identify missing settings: %v", err)
	}
}
