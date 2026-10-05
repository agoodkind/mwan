// Package cloudflaredtap implements an ifmgr log-sink module that tails
// a configured systemd unit's journal and re-emits each entry through
// the daemon's [slog.Logger]. The intent is to fold cloudflared-oob (or
// any other systemd unit) events into the same JSON log file, the same
// email-on-WARN+ pipeline, and the same trace context as everything
// else mwan-ifmgr produces.
//
// Registers as "cloudflared_tap". Selected by the oob role as an opt-in
// module; absent [ifmgr.modules.cloudflared_tap] section makes Init
// return ifmgr.ErrModuleDisabled so the daemon skips it on hosts (like
// the suburban hypervisor) that have no cloudflared-oob tunnel.
//
// The module is purely a log forwarder: Reconcile, OnKernelEvent,
// OnDHCPLease, and EvaluateAlerts are all no-ops. The work happens in
// a goroutine spawned during Init that runs journalctl as a long-lived
// child process, parses each JSON line, and re-emits.
package cloudflaredtap

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strconv"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
)

type journalEntry struct {
	Message     string `json:"MESSAGE"`
	Priority    string `json:"PRIORITY"`
	SystemdUnit string `json:"_SYSTEMD_UNIT"`
	PID         string `json:"_PID"`
	Comm        string `json:"_COMM"`
}

// Module owns the journal-tail goroutine for one unit.
type Module struct {
	ifmgr.BaseModule

	cfg      Config
	patterns []*regexp.Regexp

	running bool
	stop    chan struct{}
}

// Config is the parsed [ifmgr.modules.cloudflared_tap] sub-config.
type Config struct {
	// Unit names the systemd unit whose journal we tail. Required.
	// Example: "cloudflared-oob".
	Unit string

	// DowngradePatterns is a list of regex patterns. Any journal entry
	// whose message matches any of them is forced to DEBUG, regardless
	// of the syslog priority cloudflared assigned. Used to silence
	// well-known noisy lines like edge-rotation reconnects.
	DowngradePatterns []string

	// JournalctlPath is the binary to invoke. Defaults to "journalctl".
	// Made configurable for testing.
	JournalctlPath string
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "cloudflared_tap" }

// Init validates config, compiles regex patterns, and spawns the
// long-lived journal-tail goroutine. An empty Unit means the operator did
// not render an [ifmgr.modules.cloudflared_tap] section for this host, so
// Init returns ifmgr.ErrModuleDisabled and the daemon drops the module
// from its dispatch list.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "cloudflared_tap", "unit", m.cfg.Unit)
	log.InfoContext(ctx, "cloudflared_tap: Init",
		"unit", m.cfg.Unit,
		"downgrade_patterns", len(m.cfg.DowngradePatterns),
		"journalctl_path", m.cfg.JournalctlPath)

	if m.cfg.Unit == "" {
		log.WarnContext(ctx,
			"cloudflared_tap: missing unit; disabling module")
		return fmt.Errorf("%w: cloudflared_tap: no [ifmgr.modules.cloudflared_tap] section", ifmgr.ErrModuleDisabled)
	}

	for i, p := range m.cfg.DowngradePatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			log.WarnContext(ctx, "cloudflared_tap: invalid downgrade pattern",
				"index", i, "pattern", p, "err", err)
			return fmt.Errorf("cloudflared_tap: invalid downgrade_patterns[%d] %q: %w", i, p, err)
		}
		m.patterns = append(m.patterns, re)
	}

	if m.cfg.JournalctlPath == "" {
		m.cfg.JournalctlPath = "journalctl"
	}
	if m.cfg.JournalctlPath != "journalctl" {
		log.WarnContext(ctx, "cloudflared_tap: unsupported journalctl_path override",
			"journalctl_path", m.cfg.JournalctlPath)
		return fmt.Errorf("cloudflared_tap: journalctl_path must be \"journalctl\"")
	}

	m.stop = make(chan struct{})
	m.Lock()
	m.running = true
	m.Unlock()
	go func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			m.Log.ErrorContext(ctx, "cloudflared_tap: tailLoop panicked",
				"err", fmt.Sprint(recovered))
		}()
		m.tailLoop(ctx)
	}()
	return nil
}

// Reconcile is a no-op; this module reacts only to journal stream events.
func (m *Module) Reconcile(_ context.Context, _ *slog.Logger) error { return nil }

// tailLoop is the long-lived goroutine that runs journalctl --follow,
// parses each JSON line, and re-emits via the module logger. On
// subprocess exit it backs off exponentially (cap 30s) and retries
// until the daemon context is cancelled.
func (m *Module) tailLoop(ctx context.Context) {
	defer func() {
		m.Lock()
		m.running = false
		m.Unlock()
	}()

	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			m.Log.DebugContext(ctx, "cloudflared_tap: tail loop exiting (context cancelled)")
			return
		case <-m.stop:
			m.Log.DebugContext(ctx, "cloudflared_tap: tail loop exiting (stop signalled)")
			return
		default:
		}

		err := m.runJournalctl(ctx)
		if ctx.Err() != nil {
			return
		}

		m.Log.WarnContext(ctx, "cloudflared_tap: journalctl exited; will restart after backoff",
			"backoff", backoff.String(), "err", errMsg(err))
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runJournalctl spawns one journalctl --follow run and forwards each
// line until the subprocess exits (or context cancels). Returns the
// subprocess error.
func (m *Module) runJournalctl(ctx context.Context) error {
	args := []string{
		"-u", m.cfg.Unit,
		"-f",
		"-o", "json",
		"--no-pager",
		"-n", "0", // start at the tail; do not replay history
	}
	cmd := exec.CommandContext(ctx, "journalctl", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		m.Log.WarnContext(ctx, "cloudflared_tap: stdout pipe failed", "err", err)
		return fmt.Errorf("stdout pipe: %w", err)
	}
	if err := cmd.Start(); err != nil {
		m.Log.WarnContext(ctx, "cloudflared_tap: journalctl start failed", "err", err)
		return fmt.Errorf("start: %w", err)
	}
	m.Log.DebugContext(ctx, "cloudflared_tap: journalctl started",
		"pid", cmd.Process.Pid, "args", args)

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // up to 1 MiB per line
	for scanner.Scan() {
		m.processLine(ctx, scanner.Bytes())
	}
	scanErr := scanner.Err()
	waitErr := cmd.Wait()
	if scanErr != nil {
		m.Log.WarnContext(ctx, "cloudflared_tap: scanner failed", "err", scanErr)
		return fmt.Errorf("scanner: %w", scanErr)
	}
	if waitErr != nil {
		m.Log.WarnContext(ctx, "cloudflared_tap: journalctl wait failed", "err", waitErr)
		return fmt.Errorf("wait: %w", waitErr)
	}
	return nil
}

// processLine parses one journalctl JSON output line and re-emits it.
func (m *Module) processLine(ctx context.Context, line []byte) {
	var entry journalEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		m.Log.DebugContext(ctx, "cloudflared_tap: skip non-json line",
			"raw", string(line), "err", err.Error())
		return
	}

	msg := entry.Message
	if msg == "" {
		return
	}
	priority := parsePriority(entry.Priority)
	level := mapPriority(priority)

	// Demote routine cloudflared lines so the email handler does not page
	// on every edge rotation. Keep WARN/ERR from cloudflared at WARN/ERR
	// unless they match a downgrade pattern.
	if level < slog.LevelWarn {
		level = slog.LevelDebug
	}
	if matchAny(m.patterns, msg) {
		level = slog.LevelDebug
	}

	attrs := []slog.Attr{
		slog.String("src_unit", entry.SystemdUnit),
		slog.String("src_pid", entry.PID),
		slog.String("src_comm", entry.Comm),
		slog.Int("src_priority", priority),
		slog.String("msg", msg),
	}
	m.Log.LogAttrs(ctx, level, "cloudflared_tap", attrs...)
}

// mapPriority converts a syslog severity 0..7 to the closest [slog.Level].
//
//	0..3 (emerg/alert/crit/err) -> ERROR
//	4    (warning)              -> WARN
//	5    (notice)               -> INFO
//	6    (info)                 -> INFO
//	7    (debug)                -> DEBUG
//	any other                   -> INFO (fallback)
func mapPriority(p int) slog.Level {
	switch {
	case p <= 3:
		return slog.LevelError
	case p == 4:
		return slog.LevelWarn
	case p == 5, p == 6:
		return slog.LevelInfo
	case p == 7:
		return slog.LevelDebug
	default:
		return slog.LevelInfo
	}
}

// matchAny reports whether any pattern matches s.
func matchAny(patterns []*regexp.Regexp, s string) bool {
	for _, re := range patterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

func parsePriority(priority string) int {
	if priority == "" {
		return 0
	}
	n, err := strconv.Atoi(priority)
	if err != nil {
		return 0
	}
	return n
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// New is the Constructor registered with ifmgr. Parses cfg into Config.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		Unit:              "",
		DowngradePatterns: nil,
		JournalctlPath:    "",
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("cloudflared_tap: invalid config type %T", cfg)
		}
		c = typedConfig
	}
	return &Module{
		BaseModule: ifmgr.NewBaseModule("cloudflared_tap"),
		cfg:        c,
		patterns:   nil,
		running:    false,
		stop:       nil,
	}, nil
}

func init() { ifmgr.Register("cloudflared_tap", New) }
