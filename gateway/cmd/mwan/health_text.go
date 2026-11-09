package main

import (
	"fmt"
	"io"
	"log/slog"
	"time"

	"goodkind.io/mwan/internal/healthcheck"
)

func writeHealthText(output io.Writer, logger *slog.Logger, records []healthcheck.Record) error {
	if len(records) == 0 {
		if _, err := fmt.Fprintln(
			output,
			"Health checks did not produce any probe records.",
		); err != nil {
			logger.Warn("write empty health result failed", "err", err)
			return fmt.Errorf("write empty health result: %w", err)
		}

		return nil
	}

	for _, record := range records {
		if err := writeHealthRecordText(output, logger, record); err != nil {
			return err
		}
	}

	return nil
}

func writeHealthRecordText(output io.Writer, logger *slog.Logger, record healthcheck.Record) error {
	if _, err := fmt.Fprintf(
		output,
		"%s WAN %s (%s) via %s\n",
		record.Timestamp.Format(time.RFC3339Nano),
		record.WAN.Name,
		record.WAN.ID,
		record.WAN.Iface,
	); err != nil {
		logger.Warn("write health identity failed", "err", err)
		return fmt.Errorf("write health identity: %w", err)
	}

	if record.Ping != nil {
		attempt := record.Ping
		if _, err := fmt.Fprintf(
			output,
			"  %s ping %s attempt %d timeout %s RTT %s error %q\n",
			attempt.Family,
			attempt.Target,
			attempt.Attempt,
			attempt.Timeout,
			attempt.RTT,
			attempt.Error,
		); err != nil {
			logger.Warn("write health ping failed", "err", err)
			return fmt.Errorf("write health ping: %w", err)
		}
	}

	if record.HTTP != nil {
		attempt := record.HTTP
		if _, err := fmt.Fprintf(
			output,
			"  %s HTTP %s request %d timeout %s status %d body %q error %q\n",
			attempt.Family,
			attempt.Target,
			attempt.Iteration,
			attempt.Timeout,
			attempt.StatusCode,
			attempt.Body,
			attempt.Error,
		); err != nil {
			logger.Warn("write health HTTP failed", "err", err)
			return fmt.Errorf("write health HTTP: %w", err)
		}
	}

	return nil
}
