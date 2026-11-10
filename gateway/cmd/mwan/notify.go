package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/version"
)

// runNotify is the entry point for the `mwan notify` subcommand. It
// gives operators a one-shot way to exercise the notify package's email
// path without waiting for a real WAN flap. The validate/monitoring
// `notify-email-path-intact` check shells out to `mwan notify
// --self-test --dry-run`, so this subcommand is also the back-end for
// that monitor.
func runNotifyOptions(cfg *config.Config, flags notifyFlags, output, diagnostics io.Writer) error {
	if !flags.selfTest {
		return fmt.Errorf("notify: --self-test is required (no other modes implemented yet)")
	}

	logger, closer := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewJSONHandler(output, &slog.HandlerOptions{Level: slog.LevelInfo})}})
	defer closer.Close()
	logger = logger.With(slog.String("component", "notify-cli"))
	logger.Info("notify self-test",
		"build", version.BuildVersionString(),
		"dry_run", flags.dryRun,
		"level", flags.level.String(),
		"kind", flags.kind,
		"key", flags.key,
		"message", flags.message,
	)

	if flags.dryRun {
		return printPlannedEvent(cfg, flags, output)
	}
	return fireSelfTestEvent(cfg, logger, flags, output, diagnostics)
}

type notifyFlags struct {
	selfTest bool
	dryRun   bool
	level    slog.Level
	kind     string
	key      string
	message  string
}

// levelToken is the named string enum the notify CLI accepts via
// --level. Declaring it as its own type lets parseLevel switch on
// named constants rather than bare string literals, which the project
// staticcheck-extra rules require for any small string-literal switch.
type levelToken string

const (
	levelTokenDebug   levelToken = "DEBUG"
	levelTokenInfo    levelToken = "INFO"
	levelTokenWarn    levelToken = "WARN"
	levelTokenWarning levelToken = "WARNING"
	levelTokenError   levelToken = "ERROR"
)

// parseLevel converts the --level string to a [slog.Level]. Mirrors
// the gklog.ParseLevel set the rest of mwan uses, with a clear error
// so operators don't have to guess at invalid values.
func parseLevel(s string) (slog.Level, error) {
	switch levelToken(strings.ToUpper(strings.TrimSpace(s))) {
	case levelTokenDebug:
		return slog.LevelDebug, nil
	case levelTokenInfo:
		return slog.LevelInfo, nil
	case levelTokenWarn, levelTokenWarning:
		return slog.LevelWarn, nil
	case levelTokenError:
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("invalid --level %q (want DEBUG|INFO|WARN|ERROR)", s)
}

// defaultSelfTestKey uses the hostname so multiple hosts can self-test
// without their alert state colliding inside the notify Manager's
// per-(kind, key) state map.
func defaultSelfTestKey() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "unknown-host"
	}
	return host
}

// defaultSelfTestMessage is what shows up in the email subject when the
// operator runs `mwan notify --self-test` without overriding --message.
func defaultSelfTestMessage() string {
	return "mwan notify self-test"
}

// printPlannedEvent describes what the notifier would have done without
// touching the sink. Useful for the validate/monitoring health check
// that needs to confirm the CLI is present without actually sending.
func printPlannedEvent(cfg *config.Config, flags notifyFlags, output io.Writer) error {
	fmt.Fprintln(output, "notify self-test (dry-run): no event sent")
	fmt.Fprintf(output, "  level         = %s\n", flags.level.String())
	fmt.Fprintf(output, "  kind          = %s\n", flags.kind)
	fmt.Fprintf(output, "  key           = %s\n", flags.key)
	fmt.Fprintf(output, "  message       = %s\n", flags.message)
	if cfg == nil {
		fmt.Fprintln(output, "  email sink    = (cfg nil)")
		return nil
	}
	if cfg.Email.AlertEmail == "" || cfg.Email.SMTP2GOAPIKey == "" {
		fmt.Fprintln(output, "  email sink    = NullNotifier (cfg.Email unconfigured)")
		return nil
	}
	fmt.Fprintf(output, "  email to      = %s\n", cfg.Email.AlertEmail)
	fmt.Fprintf(output, "  subject prefix= %s\n", cfg.Email.SubjectPrefix)
	fmt.Fprintf(output, "  min_level     = %s\n", cfg.Email.MinLevel)
	if flags.level < parseMinLevelOrError(cfg.Email.MinLevel) {
		fmt.Fprintln(output, "  NOTE: event level is below cfg.Email.min_level; the sink would drop it.")
	}
	return nil
}

// parseMinLevelOrError mirrors notify.parseMinLevel but is callable from
// the CLI package. It's only used for the dry-run preview.
func parseMinLevelOrError(s string) slog.Level {
	lvl, err := parseLevel(s)
	if err != nil {
		return slog.LevelError
	}
	return lvl
}

// fireSelfTestEvent constructs the notifier via notify.FromConfig and
// emits one Event. We use a short timeout context so a wedged SMTP2GO
// transport doesn't hang the CLI indefinitely. Errors from the email
// sink itself are logged at WARN inside the Manager rather than
// surfaced through Notify, so the exit code is best-effort: zero
// means "no construction error and the event was dispatched", not
// "the SMTP2GO API returned 2xx".
func fireSelfTestEvent(cfg *config.Config, logger *slog.Logger, flags notifyFlags, output, diagnostics io.Writer) error {
	if cfg == nil {
		return fmt.Errorf("notify: cfg is nil")
	}
	notifier := notify.FromConfig(cfg.Email, cfg.NotifyConfig(), logger, "mwan-notify")
	if _, ok := notifier.(notify.NullNotifier); ok {
		fmt.Fprintln(diagnostics,
			"notify: NullNotifier in use (cfg.Email unconfigured); event will be dropped silently")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ev := notify.Event{
		Now:     time.Now(),
		Level:   flags.level,
		Kind:    flags.kind,
		Key:     flags.key,
		Message: flags.message,
		Fields: []slog.Attr{
			slog.String("source", "mwan notify --self-test"),
			slog.String("build", version.BuildVersionString()),
		},
		IsRecovery: false,
	}
	notifier.Notify(ctx, ev)
	// Immediately resolve so the self-test does not leave a stale open
	// alert in the Manager state map.
	notifier.Resolve(ctx, flags.kind, flags.key, "self-test complete")
	fmt.Fprintln(output, "notify self-test dispatched (check journald + inbox for delivery)")
	return nil
}
