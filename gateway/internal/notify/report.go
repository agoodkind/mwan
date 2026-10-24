package notify

import (
	"context"
	"errors"
	"fmt"

	"goodkind.io/send-email/mailer"
)

// SendReport applies report addressing without suppressing delivery failures.
func (manager *Manager) SendReport(ctx context.Context, message mailer.Message) error {
	sink, ok := manager.sink.(*emailSink)
	if !ok {
		return errors.New("configure email delivery and alert_email for reports")
	}
	if err := sink.SendMessage(ctx, message); err != nil {
		manager.log.WarnContext(ctx, "report email delivery failed", "error", err)
		return fmt.Errorf("send report email: %w", err)
	}
	return nil
}
