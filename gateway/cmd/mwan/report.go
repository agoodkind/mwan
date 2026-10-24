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
	if err != nil && !errors.Is(err, context.Canceled) {
		fmt.Fprintf(errorOutput, "mwan report: %v\n", err)
		return 1
	}
	if flags.action == "send" {
		fmt.Fprintln(output, "The email transport accepted the report.")
	}
	return 0
}

func executeReport(ctx context.Context, flags reportFlags, cfg *config.Config) error {
	if cfg == nil {
		return errors.New("reporting requires a configuration")
	}
	if err := cfg.Report.Validate(); err != nil {
		return err
	}
	if cfg.Email.AlertEmail == "" || (cfg.Email.SMTP2GOAPIKey == "" && cfg.Email.MsmtprcPath == "") {
		return errors.New("configure email delivery and alert_email for reports")
	}
	network, err := networkload.Load(flags.networkPath, flags.schemaDir)
	if err != nil {
		return err
	}
	logger, closer := logging.New(logging.Config{BuildVersion: version.BuildVersionString(), Handlers: []slog.Handler{logging.StdoutJSONLevel(slog.LevelInfo)}})
	defer closer.Close()
	notifications, err := notify.New(cfg.Email, cfg.NotifyConfig(), logger, "mwan-report")
	if err != nil {
		return err
	}
	service, err := reporting.NewService(reporting.Options{Config: cfg.Report, Network: network, Notifications: notifications, Log: logger})
	if err != nil {
		return err
	}
	defer service.Close()
	if flags.action == "send" {
		timeout, _ := time.ParseDuration(cfg.Report.MailTimeout)
		sendContext, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return service.SendDaily(sendContext)
	}
	collector, err := reporting.NewCollector(cfg.Report, network, logger)
	if err != nil {
		return err
	}
	defer collector.Close()
	return service.Watch(ctx, collector)
}
