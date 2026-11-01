package main

import (
	"bytes"
	"html"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/testsmtp"
)

func TestNotifySelfTestUsesNoticeAndMinimumLevel(t *testing.T) {
	for _, example := range []struct {
		level   slog.Level
		deliver bool
	}{{slog.LevelError, true}, {slog.LevelWarn, false}} {
		t.Run(example.level.String(), func(t *testing.T) {
			t.Parallel()
			server := testsmtp.Start(t)
			configuration := &config.Config{Email: notify.EmailConfig{
				MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", SubjectPrefix: "[Suburban]", MinLevel: "ERROR",
			}}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			if err := fireSelfTestEvent(configuration, logger, notifyFlags{
				selfTest: true, level: example.level, kind: "self-test", key: "test", message: "The notification self-test started.",
			}); err != nil {
				t.Fatal(err)
			}
			if !example.deliver {
				select {
				case data := <-server.Messages:
					t.Fatalf("below-threshold self-test delivered an email:\n%s", data)
				default:
				}
				return
			}
			for _, subject := range []string{"[Suburban] Notice: The notification self-test started.", "[Suburban] Recovered: self-test complete"} {
				data := server.Next(t)
				message, err := mail.ReadMessage(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				if message.Header.Get("Subject") != subject {
					t.Fatalf("self-test subject=%q, want %q", message.Header.Get("Subject"), subject)
				}
				text := html.UnescapeString(testsmtp.HTML(t, data))
				if strings.Contains(text, "failure observed") || strings.Contains(text, "Observed duration") {
					t.Fatalf("self-test fabricated failure history:\n%s", text)
				}
			}
		})
	}
}
