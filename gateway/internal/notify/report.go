package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"goodkind.io/send-email/mailer"
)

// SendReport applies report addressing without suppressing delivery failures.
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
	if err := manager.reportSender.SendMessage(ctx, message); err != nil {
		manager.log.WarnContext(ctx, "report email delivery failed", "error", err)
		return fmt.Errorf("send report email: %w", err)
	}
	return nil
}
