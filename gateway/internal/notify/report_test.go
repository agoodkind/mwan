package notify_test

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/testsmtp"
	"goodkind.io/send-email/mailer"
)

func TestSendReportReturnsAcceptance(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	manager, err := notify.New(notify.EmailConfig{MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", SubjectPrefix: "[lab]", MinLevel: "error"}, notify.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "mwan-report")
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
	manager, err := notify.New(notify.EmailConfig{}, notify.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)), "mwan-report")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SendReport(t.Context(), mailer.Message{Subject: "Report", Body: "Measurements"}); err == nil {
		t.Fatal("unconfigured report delivery succeeded")
	}
}
