// Package wg polls a WireGuard interface and reports per-peer
// handshake age plus byte-rate health. Two modes:
//
//   - Remote SSH mode: when ssh_host is set, runs `wg show <iface> dump`
//     on the remote (typically OPNsense). Used on vault by the oob role.
//   - Local exec mode: when ssh_host is empty, runs `wg show <iface> dump`
//     locally. Used by daemons running on a WG endpoint host directly
//     (e.g. suburban hypervisor) via the same oob role.
//
// The module emits one structured log entry per peer per Reconcile pass
// at DEBUG. Threshold-based alerts fire at WARN when a peer handshake
// goes stale and at ERROR when it crosses a higher threshold. Recovery
// emits INFO and clears the alert.
//
// For bidirectional split-brain detection (each side's view of the same
// peer should agree on endpoint after NAT normalization), run wg on
// BOTH sides. Each daemon emits its local view as structured logs
// with module=wg and src_host=<hostname>. Cross-side correlation is
// currently log-analysis.
//
// Registers as "wg".
package wg

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/execabs"
	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
)

// Module owns wg state.
type Module struct {
	ifmgr.BaseModule

	cfg Config
	// clock is the wall-clock seam used for handshake-age comparisons and
	// command timing so tests can drive time deterministically.
	clock internalclock.Clock

	// disabled is true when New received a nil ModuleConfig, meaning the
	// adapter saw no [ifmgr.modules.wg] section. Init returns
	// ifmgr.ErrModuleDisabled in that case so the daemon drops the
	// module from its dispatch list. A present-but-default Config (e.g.
	// local-exec mode on wg0 with no ssh_host) is still a valid
	// configuration and must NOT trip this sentinel.
	disabled bool

	// runWGShow is the test seam for runRemoteWGShow. Production code leaves
	// this nil and the real implementation runs. Tests assign a stub before
	// calling Reconcile to avoid execing ssh or wg.
	runWGShow func(ctx context.Context, log *slog.Logger) (string, error)

	lastPeers map[string]peerState // key = peer pubkey
	lastRunAt time.Time
}

// Config is the parsed [ifmgr.modules.wg] sub-config.
type Config struct {
	// SSHHost is the remote target for `wg show <iface> dump`. Format:
	// "user@host" (e.g. "agoodkind@3d06:bad:b01::1"). The user must be
	// allowed to run the wg-show command non-interactively (key auth + sudo
	// NOPASSWD as needed).
	SSHHost string

	// SSHPort optional override; default 22.
	SSHPort int

	// IdentityFile optional SSH private key path. If empty, ssh uses
	// its default search. Set explicitly when running under a sandboxed
	// systemd unit that hides /root/.ssh.
	IdentityFile string

	// Iface to inspect on the remote side; default "wg0".
	Iface string

	// Sudo: if true, prefix the remote command with "sudo -n". Required
	// for OPNsense + FreeBSD where wg show needs root.
	Sudo bool

	// PollInterval between Reconcile passes; default ifmgr Reconcile cadence.
	// (kept here for documentation; actual cadence is governed by the daemon.)

	// WarnHandshakeAge: per-peer handshake age above which a WARN alert fires.
	// Default 180s.
	WarnHandshakeAge time.Duration

	// ErrorHandshakeAge: per-peer handshake age above which an ERROR alert fires.
	// Default 300s.
	ErrorHandshakeAge time.Duration

	// IgnorePeers is a list of peer public keys to skip (e.g. mobile peers
	// that legitimately stay idle for long stretches and should not alert).
	IgnorePeers map[string]bool

	// Timeout for the SSH+wg-show command.
	Timeout time.Duration
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "wg" }

type peerState struct {
	endpoint  string
	rxBytes   int64
	txBytes   int64
	handshake time.Time // zero if peer has never handshaked
	keepalive time.Duration
}

// Init implements ifmgr.Module.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	if m.disabled {
		log := m.InitBase(env, "module", "wg")
		log.InfoContext(ctx, "wg: Init (disabled)")
		return fmt.Errorf("%w: wg: no [ifmgr.modules.wg] section", ifmgr.ErrModuleDisabled)
	}
	mode := "local"
	if m.cfg.SSHHost != "" {
		mode = "ssh"
	}
	log := m.InitBase(env, "module", "wg", "mode", mode, "ssh_host", m.cfg.SSHHost, "iface", m.cfg.Iface)
	log.InfoContext(
		ctx, "wg: Init",
		"warn_handshake_age", m.cfg.WarnHandshakeAge.String(),
		"error_handshake_age", m.cfg.ErrorHandshakeAge.String(),
		"ignored_peer_count", len(m.cfg.IgnorePeers),
		"sudo", m.cfg.Sudo,
		"timeout", m.cfg.Timeout.String(),
	)
	if m.cfg.Iface == "" {
		log.WarnContext(ctx, "wg: Init missing iface")
		return fmt.Errorf("wg: iface is required")
	}
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	m.lastPeers = map[string]peerState{}
	return nil
}

// Reconcile fetches the current peer table from the remote and updates state.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	if m.clock == nil {
		m.clock = internalclock.Real{}
	}
	now := m.clock.Now()
	runner := m.runWGShow
	if runner == nil {
		runner = m.runRemoteWGShow
	}
	out, err := runner(ctx, log)
	if err != nil {
		// Route through AlertManager so repeated failures collapse to a
		// single transition email plus one recovery email when SSH comes
		// back, instead of one log.Error per ~6 minutes governed by the
		// gklog email subject cooldown.
		m.Env.Alerts.NotifyContext(
			ctx, now, slog.LevelError,
			"wg-reconcile-failed", "remote-wg-show",
			"wg: remote wg show failed",
			slog.String("err", err.Error()),
		)
		return nil // do not fail the whole reconcile loop
	}
	peers, parseErr := parseWGShowDump(out)
	if parseErr != nil {
		m.Env.Alerts.NotifyContext(
			ctx, now, slog.LevelError,
			"wg-reconcile-failed", "parse-wg-dump",
			"wg: parse wg dump failed",
			slog.String("err", parseErr.Error()),
			slog.Int("raw_lines", strings.Count(out, "\n")),
		)
		return nil
	}
	// Healthy tick: clear any previously-active reconcile-failure alert so the
	// inbox sees a recovery email. Resolve is a no-op when no alert is active.
	m.Env.Alerts.ResolveContext(
		ctx, now,
		"wg-reconcile-failed", "remote-wg-show",
		"wg: remote wg show recovered",
	)
	m.Env.Alerts.ResolveContext(
		ctx, now,
		"wg-reconcile-failed", "parse-wg-dump",
		"wg: parse wg dump recovered",
	)
	m.Lock()
	m.lastPeers = peers
	m.lastRunAt = now
	m.Unlock()
	for pubkey, p := range peers {
		ageStr := "never"
		ageS := -1
		if !p.handshake.IsZero() {
			age := now.Sub(p.handshake)
			ageS = int(age.Seconds())
			ageStr = age.Truncate(time.Second).String()
		}
		log.DebugContext(
			ctx, "wg: peer",
			"peer", shortKey(pubkey),
			"endpoint", p.endpoint,
			"handshake_age", ageStr,
			"handshake_age_s", ageS,
			"rx_bytes", p.rxBytes,
			"tx_bytes", p.txBytes,
			"keepalive_s", int(p.keepalive.Seconds()),
			"ignored", m.cfg.IgnorePeers[pubkey],
		)
	}
	log.DebugContext(ctx, "wg: reconcile complete", "peer_count", len(peers))
	return nil
}

// EvaluateAlerts emits per-peer WARN/ERROR/recovery transitions.
func (m *Module) EvaluateAlerts(ctx context.Context, log *slog.Logger, now time.Time) {
	m.Lock()
	peers := make(map[string]peerState, len(m.lastPeers))
	maps.Copy(peers, m.lastPeers)
	last := m.lastRunAt
	m.Unlock()
	if last.IsZero() {
		return
	}
	for pubkey, p := range peers {
		if m.cfg.IgnorePeers[pubkey] {
			continue
		}
		key := shortKey(pubkey)
		if p.handshake.IsZero() {
			// Peer has never handshaked. This is the normal state for
			// mobile/laptop WG clients that come online intermittently.
			// Alerting on these would create permanent false positives.
			// A peer that previously handshaked and went silent is the
			// real failure mode worth alerting on; this module only
			// observes the OPNsense view, so we cannot distinguish a
			// peer that "never handshaked since daemon start" from one
			// that "never handshaked ever". Skipping is the safe default.
			// Use ignore_peers explicitly if a peer should not be tracked.
			log.DebugContext(ctx, "wg: peer has never handshaked, skipping alert",
				"peer", key, "endpoint", p.endpoint)
			continue
		}
		age := now.Sub(p.handshake)
		switch {
		case age >= m.cfg.ErrorHandshakeAge:
			m.Env.Alerts.NotifyContext(
				ctx,
				now, slog.LevelError,
				"wg-peer-stalled", key,
				"wg: peer handshake stalled past error threshold",
				slog.String("peer", key),
				slog.String("endpoint", p.endpoint),
				slog.Int("handshake_age_s", int(age.Seconds())),
				slog.Int("threshold_s", int(m.cfg.ErrorHandshakeAge.Seconds())),
			)
		case age >= m.cfg.WarnHandshakeAge:
			m.Env.Alerts.NotifyContext(
				ctx,
				now, slog.LevelWarn,
				"wg-peer-stalled", key,
				"wg: peer handshake stalled past warn threshold",
				slog.String("peer", key),
				slog.String("endpoint", p.endpoint),
				slog.Int("handshake_age_s", int(age.Seconds())),
				slog.Int("threshold_s", int(m.cfg.WarnHandshakeAge.Seconds())),
			)
		default:
			if m.Env.Alerts.Active("wg-peer-stalled", key) {
				m.Env.Alerts.ResolveContext(
					ctx,
					now,
					"wg-peer-stalled", key,
					"wg: peer handshake recovered",
					slog.String("peer", key),
					slog.String("endpoint", p.endpoint),
					slog.Int("handshake_age_s", int(age.Seconds())),
				)
			}
		}
		_ = log
	}
}

// runRemoteWGShow runs `wg show <iface> dump` either locally (when SSHHost
// is empty) or on the configured ssh target, and returns the raw stdout.
func (m *Module) runRemoteWGShow(ctx context.Context, log *slog.Logger) (string, error) {
	timeout := m.cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if m.cfg.SSHHost == "" {
		return m.runLocalWGShow(ctx, log, timeout)
	}
	port := m.cfg.SSHPort
	if port == 0 {
		port = 22
	}
	// Note: UserKnownHostsFile=/dev/null + StrictHostKeyChecking=no avoids
	// writes to /root/.ssh/known_hosts. Required because the daemon runs
	// under systemd ProtectHome= which makes that path inaccessible.
	// IdentityFile is explicit so the daemon does not depend on the SSH
	// agent or the default search path.
	args := []string{
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=5",
		"-o", "ServerAliveInterval=3",
		"-o", "ServerAliveCountMax=2",
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-p", strconv.Itoa(port),
	}
	if m.cfg.IdentityFile != "" {
		args = append(args, "-i", m.cfg.IdentityFile)
	}
	args = append(args, m.cfg.SSHHost)
	remoteCmd := fmt.Sprintf("wg show %s dump", m.cfg.Iface)
	if m.cfg.Sudo {
		remoteCmd = "sudo -n " + remoteCmd
	}
	args = append(args, remoteCmd)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := m.clock.Now()
	cmd := exec.CommandContext(cctx, "ssh", args...)
	out, err := cmd.Output()
	dur := m.clock.Now().Sub(start)
	if err != nil {
		stderr := ""
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		log.WarnContext(
			ctx, "wg: ssh wg show failed",
			"duration_ms", dur.Milliseconds(),
			"err", err,
			"stderr", stderr,
		)
		return "", fmt.Errorf("ssh %s: %w (stderr=%q)", m.cfg.SSHHost, err, stderr)
	}
	log.DebugContext(
		ctx,
		"wg: ssh wg show ok",
		"duration_ms", dur.Milliseconds(),
		"out_bytes", len(out),
	)
	return string(out), nil
}

// runLocalWGShow runs `wg show <iface> dump` directly on the local host.
// Used when SSHHost is empty (local-exec mode). Optionally wraps in sudo
// if the daemon does not run as root and wg requires elevation.
func (m *Module) runLocalWGShow(ctx context.Context, log *slog.Logger, timeout time.Duration) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var cmd *exec.Cmd
	if m.cfg.Sudo {
		cmd = execabs.CommandContext(cctx, "sudo", "-n", "wg", "show", m.cfg.Iface, "dump")
	} else {
		cmd = execabs.CommandContext(cctx, "wg", "show", m.cfg.Iface, "dump")
	}
	start := m.clock.Now()
	out, err := cmd.Output()
	dur := m.clock.Now().Sub(start)
	if err != nil {
		stderr := ""
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		log.WarnContext(
			ctx, "wg: local wg show failed",
			"duration_ms", dur.Milliseconds(),
			"iface", m.cfg.Iface,
			"sudo", m.cfg.Sudo,
			"err", err,
			"stderr", stderr,
		)
		return "", fmt.Errorf("local wg show %s: %w (stderr=%q)", m.cfg.Iface, err, stderr)
	}
	log.DebugContext(
		ctx,
		"wg: local wg show ok",
		"duration_ms", dur.Milliseconds(),
		"iface", m.cfg.Iface,
		"out_bytes", len(out),
	)
	return string(out), nil
}

// parseWGShowDump parses the multi-line `wg show <iface> dump` format.
//
// Line 1 (interface): "<priv> <pub> <listen-port> <fwmark>"
// Line 2+: "<peer-pub> <psk> <endpoint> <allowed-ips> <last-handshake-epoch> <rx> <tx> <persistent-keepalive>"
//
// "(none)" is used by wg for absent values. "off" appears for keepalive when disabled.
// "0" handshake epoch means the peer has never completed a handshake.
func parseWGShowDump(s string) (map[string]peerState, error) {
	out := map[string]peerState{}
	scanner := bufio.NewScanner(strings.NewReader(s))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimRight(scanner.Text(), "\r\n")
		if line == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if lineNo == 1 {
			continue // interface header line
		}
		if len(fields) < 8 {
			slog.Warn("wg: malformed dump line", "line_no", lineNo, "line", line)
			return nil, fmt.Errorf("line %d: want 8 tab-separated fields, got %d (%q)", lineNo, len(fields), line)
		}
		pubkey := fields[0]
		endpoint := fields[2]
		hsEpoch, err := strconv.ParseInt(fields[4], 10, 64)
		if err != nil {
			slog.Warn("wg: invalid handshake epoch", "line_no", lineNo, "value", fields[4], "err", err)
			return nil, fmt.Errorf("line %d: handshake epoch %q: %w", lineNo, fields[4], err)
		}
		rx, err := strconv.ParseInt(fields[5], 10, 64)
		if err != nil {
			slog.Warn("wg: invalid rx bytes", "line_no", lineNo, "value", fields[5], "err", err)
			return nil, fmt.Errorf("line %d: rx %q: %w", lineNo, fields[5], err)
		}
		tx, err := strconv.ParseInt(fields[6], 10, 64)
		if err != nil {
			slog.Warn("wg: invalid tx bytes", "line_no", lineNo, "value", fields[6], "err", err)
			return nil, fmt.Errorf("line %d: tx %q: %w", lineNo, fields[6], err)
		}
		var ka time.Duration
		switch keepaliveToken(fields[7]) {
		case keepaliveOff, keepaliveZero:
			ka = 0
		default:
			n, err := strconv.Atoi(fields[7])
			if err != nil {
				slog.Warn("wg: invalid keepalive", "line_no", lineNo, "value", fields[7], "err", err)
				return nil, fmt.Errorf("line %d: keepalive %q: %w", lineNo, fields[7], err)
			}
			ka = time.Duration(n) * time.Second
		}
		ps := peerState{
			endpoint:  endpoint,
			rxBytes:   rx,
			txBytes:   tx,
			keepalive: ka,
			handshake: time.Time{},
		}
		if hsEpoch > 0 {
			ps.handshake = time.Unix(hsEpoch, 0)
		}
		out[pubkey] = ps
	}
	if err := scanner.Err(); err != nil {
		slog.Warn("wg: scanner failed", "err", err)
		return nil, fmt.Errorf("scan: %w", err)
	}
	return out, nil
}

type keepaliveToken string

const (
	keepaliveOff  keepaliveToken = "off"
	keepaliveZero keepaliveToken = "0"
)

// shortKey returns a short, log-friendly form of the peer pubkey
// (first 8 chars) so log messages stay readable. Pubkeys are 44 chars
// in base64 form.
func shortKey(pub string) string {
	if len(pub) <= 8 {
		return pub
	}
	return pub[:8]
}

// New is the constructor. A nil cfg means the operator did not render an
// [ifmgr.modules.wg] section for this host; the returned Module remembers
// this and Init returns ifmgr.ErrModuleDisabled. A non-nil cfg even with
// every field at zero is a valid local-exec config (Iface defaults to
// wg0) and must not flip the disabled flag.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		SSHHost:           "",
		SSHPort:           0,
		IdentityFile:      "",
		Iface:             "wg0",
		Sudo:              false,
		WarnHandshakeAge:  180 * time.Second,
		ErrorHandshakeAge: 300 * time.Second,
		Timeout:           10 * time.Second,
		IgnorePeers:       map[string]bool{},
	}
	if cfg == nil {
		return &Module{
			BaseModule: ifmgr.NewBaseModule("wg"),
			cfg:        c,
			clock:      nil,
			disabled:   true,
			runWGShow:  nil,
			lastPeers:  nil,
			lastRunAt:  time.Time{},
		}, nil
	}
	typedConfig, ok := cfg.(Config)
	if !ok {
		return nil, fmt.Errorf("wg: invalid config type %T", cfg)
	}
	if typedConfig.Iface == "" {
		typedConfig.Iface = c.Iface
	}
	if typedConfig.IgnorePeers == nil {
		typedConfig.IgnorePeers = map[string]bool{}
	}
	return &Module{
		BaseModule: ifmgr.NewBaseModule("wg"),
		cfg:        typedConfig,
		clock:      nil,
		disabled:   false,
		runWGShow:  nil,
		lastPeers:  nil,
		lastRunAt:  time.Time{},
	}, nil
}

func init() { ifmgr.Register("wg", New) }
