package email_test

import (
	"bytes"
	"image"
	"image/png"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/email"
	"goodkind.io/mwan/internal/testsmtp"
	"goodkind.io/send-email/mailer"
)

func TestSendMessagePreservesRichContent(t *testing.T) {
	t.Parallel()
	server := testsmtp.Start(t)
	sender, err := email.NewSenderFromConfig(email.SenderConfig{
		Mail: mailer.Config{MsmtprcPath: server.Path}, AttemptTimeout: 5 * time.Second,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	message := mailer.Message{To: "recipient@example.com", Subject: "Report", Content: []mailer.ContentBlock{
		{Kind: mailer.ContentHTML, HTML: `<img src="cid:usage.png" alt="Usage">`, Text: "Usage graph"},
		{Kind: mailer.ContentTable, Table: &mailer.Table{Caption: "Top talkers", Headers: []string{"Client"}, Rows: [][]string{{"lab"}}}},
	}, Inlines: []mailer.InlineImage{{Filename: "usage.png", MIMEType: "image/png", Data: encoded.Bytes()}}, Attachments: []mailer.Attachment{{Filename: "diagnostics.txt", MIMEType: "text/plain", Data: []byte("diagnostics")}}}
	if err := sender.SendMessage(t.Context(), message); err != nil {
		t.Fatal(err)
	}
	accepted := server.Next(t)
	html := testsmtp.HTML(t, accepted)
	for _, text := range []string{"cid:usage.png", "Top talkers", ">Memory<", ">Load<"} {
		if !strings.Contains(html, text) {
			t.Fatalf("accepted HTML omitted %q", text)
		}
	}
	if !bytes.Contains(accepted, []byte("diagnostics.txt")) || !bytes.Contains(accepted, []byte("Content-ID: <usage.png>")) {
		t.Fatal("accepted message omitted its attachment or inline image")
	}
	server.Reject(true)
	if err := sender.SendMessage(t.Context(), message); err == nil {
		t.Fatal("SMTP rejection did not fail delivery")
	}
}
