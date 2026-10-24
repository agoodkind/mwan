package notify

import (
	"context"
	"errors"
	"strings"

	"goodkind.io/send-email/mailer"
)

func (manager *Manager) SendReport(ctx context.Context, message mailer.Message) error {
	if manager.reportSender == nil {
		return errors.New("configure email delivery and alert_email for reports")
	}
	if message.To == "" {
		message.To = manager.reportRecipient
	}
	if manager.reportPrefix != "" && !strings.HasPrefix(message.Subject, manager.reportPrefix) {
		message.Subject = manager.reportPrefix + " " + message.Subject
	}
	return manager.reportSender.SendMessage(ctx, message)
}
