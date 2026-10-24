package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/reporting"
	"goodkind.io/mwan/internal/version"
)

func runReport(ctx context.Context, arguments []string, cfg *config.Config, output, errorOutput io.Writer) int {
	flags, err := parseReportFlags(arguments, errorOutput)
	if err == nil {
		err = executeReport(ctx, flags, cfg)
	}
	if errors.Is(err, context.Canceled) && flags.action == "watch" {
		return 0
	}
	if err != nil {
		fmt.Fprintf(errorOutput, "mwan report: %v\n", err)
		return 1
	}
	if flags.action == "send" {
		fmt.Fprintln(output, "The email transport accepted the report.")
	}
	return 0
}

func executeReport(ctx context.Context, flags reportFlags, cfg *config.Config) (reportError error) {
	defer func() {
		if reportError != nil {
			slog.ErrorContext(ctx, "report command failed", "action", flags.action, "error", reportError)
		}
	}()
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start reporting command: %w", err)
	}
	if cfg == nil {
		return errors.New("reporting requires a configuration")
	}
	if err := cfg.Report.Validate(); err != nil {
		return fmt.Errorf("validate reporting settings: %w", err)
	}
	if cfg.Email.AlertEmail == "" || (cfg.Email.SMTP2GOAPIKey == "" && cfg.Email.MsmtprcPath == "") {
		return errors.New("configure email delivery and alert_email for reports")
	}
	network, err := networkload.Load(flags.networkPath, flags.schemaDir)
	if err != nil {
		return fmt.Errorf("load reporting network: %w", err)
	}
	logger, closer := logging.New(logging.Config{BuildVersion: version.BuildVersionString(), Handlers: []slog.Handler{logging.StdoutJSONLevel(slog.LevelInfo)}})
	defer closer.Close()
	notifications, err := notify.New(cfg.Email, cfg.NotifyConfig(), logger, "mwan-report")
	if err != nil {
		return fmt.Errorf("configure report notifications: %w", err)
	}
	service, err := reporting.NewService(reporting.Options{Config: cfg.Report, Network: network, Notifications: notifications, Log: logger})
	if err != nil {
		return fmt.Errorf("open reporting service: %w", err)
	}
	defer service.Close()
	if flags.action == "send" {
		timeout, _ := time.ParseDuration(cfg.Report.MailTimeout)
		sendContext, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		if err := service.SendDaily(sendContext); err != nil {
			return fmt.Errorf("send daily report: %w", err)
		}
		return nil
	}
	collector, err := reporting.NewCollector(cfg.Report, network, logger)
	if err != nil {
		return fmt.Errorf("open reporting collector: %w", err)
	}
	defer collector.Close()
	if err := service.Watch(ctx, collector); err != nil {
		return fmt.Errorf("watch reporting measurements: %w", err)
	}
	return nil
}
