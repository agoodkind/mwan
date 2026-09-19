package wg

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/notify"
)

// canonical wg show wg0 dump output: first line is the interface, rest are peers.
// Field separators are tabs.
const sampleDump = "PRIVKEYabc=\tjz3eKGui8bC2vf9rxrKGbk6WGwQWIc/PRNpFW91xDkk=\t51820\toff\n" +
	"EYvlZyous8zcgLwT09khnXd3VPQj2sbphlrY1BpErXw=\t(none)\t[2601:84:837c:a160:3e8c:f8ff:fef9:7bf2]:51821\t10.240.0.0/16,3d06:bad:b01:200::/56\t1777386334\t903942\t815478\t25\n" +
	"NeverPeer1234abcd=\t(none)\t(none)\t10.240.10.99/32\t0\t0\t0\toff\n" +
	"PmJXT8pLFCkJwRCSkKhXAJJTs598tiQdvR+kKQeORkI=\t(none)\t(none)\t3d06:bad:b01:a::8/128\t1777380000\t512\t1024\t25\n"

func TestParseWGShowDump(t *testing.T) {
	got, err := parseWGShowDump(sampleDump)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 peers, got %d", len(got))
	}
	sub, ok := got["EYvlZyous8zcgLwT09khnXd3VPQj2sbphlrY1BpErXw="]
	if !ok {
		t.Fatalf("suburban peer missing")
	}
	if sub.endpoint != "[2601:84:837c:a160:3e8c:f8ff:fef9:7bf2]:51821" {
		t.Errorf("endpoint=%q", sub.endpoint)
	}
	if sub.handshake.Unix() != 1777386334 {
		t.Errorf("handshake epoch=%d want 1777386334", sub.handshake.Unix())
	}
	if sub.rxBytes != 903942 || sub.txBytes != 815478 {
		t.Errorf("rx=%d tx=%d", sub.rxBytes, sub.txBytes)
	}
	if sub.keepalive != 25*time.Second {
		t.Errorf("keepalive=%s", sub.keepalive)
	}
	never, ok := got["NeverPeer1234abcd="]
	if !ok {
		t.Fatalf("never-handshaked peer missing")
	}
	if !never.handshake.IsZero() {
		t.Errorf("never peer should have zero handshake, got %v", never.handshake)
	}
	if never.keepalive != 0 {
		t.Errorf("never peer keepalive should be 0 (off), got %s", never.keepalive)
	}
}

func TestParseWGShowDump_EmptyAndHeaderOnly(t *testing.T) {
	if got, err := parseWGShowDump(""); err != nil || len(got) != 0 {
		t.Errorf("empty: got=%v err=%v", got, err)
	}
	headerOnly := "PRIVKEYabc=\tPUB\t51820\toff\n"
	if got, err := parseWGShowDump(headerOnly); err != nil || len(got) != 0 {
		t.Errorf("header only: got=%v err=%v", got, err)
	}
}

func TestParseWGShowDump_BadFields(t *testing.T) {
	cases := map[string]string{
		"too few fields": "PRIVKEYabc=\tPUB\t51820\toff\nshort\n",
		"bad epoch":      "PRIVKEYabc=\tPUB\t51820\toff\nPK1=\t(none)\t(none)\t10/32\tNOT_A_NUMBER\t0\t0\toff\n",
		"bad keepalive":  "PRIVKEYabc=\tPUB\t51820\toff\nPK1=\t(none)\t(none)\t10/32\t0\t0\t0\tnotaduration\n",
	}
	for name, input := range cases {
		if _, err := parseWGShowDump(input); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestShortKey(t *testing.T) {
	if got := shortKey("EYvlZyous8zcgLwT09khnXd3VPQj2sbphlrY1BpErXw="); got != "EYvlZyou" {
		t.Errorf("shortKey: %q", got)
	}
	if got := shortKey("short"); got != "short" {
		t.Errorf("shortKey short: %q", got)
	}
}

func TestNew_DefaultsAndOverrides(t *testing.T) {
	m, err := New(Config{
		SSHHost:           "agoodkind@3d06:bad:b01::1",
		Sudo:              true,
		WarnHandshakeAge:  200 * time.Second,
		ErrorHandshakeAge: 400 * time.Second,
		IgnorePeers: map[string]bool{
			"PmJXT8pLFCkJwRCSkKhXAJJTs598tiQdvR+kKQeORkI=": true,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	mod := m.(*Module)
	if mod.cfg.SSHHost != "agoodkind@3d06:bad:b01::1" {
		t.Errorf("ssh_host=%q", mod.cfg.SSHHost)
	}
	if !mod.cfg.Sudo {
		t.Errorf("sudo=false, want true")
	}
	if mod.cfg.Iface != "wg0" {
		t.Errorf("iface default not wg0: %q", mod.cfg.Iface)
	}
	if mod.cfg.WarnHandshakeAge != 200*time.Second {
		t.Errorf("warn_handshake_age=%s", mod.cfg.WarnHandshakeAge)
	}
	if mod.cfg.ErrorHandshakeAge != 400*time.Second {
		t.Errorf("error_handshake_age=%s", mod.cfg.ErrorHandshakeAge)
	}
	if !mod.cfg.IgnorePeers["PmJXT8pLFCkJwRCSkKhXAJJTs598tiQdvR+kKQeORkI="] {
		t.Errorf("ignore_peers not set")
	}
}

func TestNew_RequiresSSHHost(t *testing.T) {
	m, err := New(Config{})
	if err != nil {
		t.Fatalf("constructor should not error on missing ssh_host (validated in Init): %v", err)
	}
	// Init should reject:
	mod := m.(*Module)
	if mod.cfg.SSHHost != "" {
		t.Errorf("expected empty ssh_host, got %q", mod.cfg.SSHHost)
	}
}

// guard against accidentally counting the header line as a peer when input
// has CRLF line endings.
func TestParseWGShowDump_CRLF(t *testing.T) {
	crlf := strings.ReplaceAll(sampleDump, "\n", "\r\n")
	got, err := parseWGShowDump(crlf)
	if err != nil {
		t.Fatalf("CRLF: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("CRLF want 3 peers, got %d", len(got))
	}
}

// captureHandler records every slog.Record it receives so tests can assert
// that AlertManager emitted exactly the expected number of records for a
// given (kind, key). Pattern mirrors internal/ifmgr/alerts_test.go.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// recordsMatching returns the subset of captured records whose alert_kind
// and alert_key attributes both match the given values. AlertManager always
// attaches both attributes, so this is the canonical way to filter.
func recordsMatching(records []slog.Record, kind, key string) []slog.Record {
	var out []slog.Record
	for _, r := range records {
		var gotKind, gotKey string
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "alert_kind":
				gotKind = a.Value.String()
			case "alert_key":
				gotKey = a.Value.String()
			}
			return true
		})
		if gotKind == kind && gotKey == key {
			out = append(out, r)
		}
	}
	return out
}

// newModuleForReconcileTest builds a Module wired to a captureHandler-backed
// AlertManager so tests can assert on Notify and Resolve transitions without
// touching ssh, exec, or filesystem.
func newModuleForReconcileTest(t *testing.T) (*Module, *captureHandler) {
	t.Helper()
	h := &captureHandler{}
	log := slog.New(h)
	alerts := ifmgr.WrapNotifier(notify.FromConfig(&config.Config{}, log, "mwan-ifmgr"))
	env := &ifmgr.Env{
		Iface:  "wg0",
		Log:    log,
		Alerts: alerts,
	}
	m := &Module{
		BaseModule: ifmgr.NewBaseModule("wg"),
		cfg:        Config{Iface: "wg0"},
		lastPeers:  map[string]peerState{},
	}
	m.InitBase(env)
	return m, h
}

// Three consecutive Reconcile passes where the wg-show seam returns an
// error must collapse to exactly one alert emit on the (wg-reconcile-failed,
// remote-wg-show) (kind, key) pair: the transition. AlertManager is the
// gate that keeps the email sender from re-firing every reconcile tick.
func TestReconcile_RemoteWGShowFailure_CollapsesToSingleEmit(t *testing.T) {
	m, h := newModuleForReconcileTest(t)
	m.runWGShow = func(_ context.Context, _ *slog.Logger) (string, error) {
		return "", errors.New("ssh: connection refused")
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := m.Reconcile(ctx, m.Log); err != nil {
			t.Fatalf("Reconcile #%d returned err: %v", i+1, err)
		}
	}
	matched := recordsMatching(h.snapshot(), "wg-reconcile-failed", "remote-wg-show")
	if len(matched) != 1 {
		t.Fatalf("got %d records for (wg-reconcile-failed, remote-wg-show), want 1", len(matched))
	}
	if matched[0].Level != slog.LevelError {
		t.Errorf("emit level=%v, want ERROR", matched[0].Level)
	}
}

// After three failing Reconciles, a successful Reconcile must emit a
// Resolve record for (wg-reconcile-failed, remote-wg-show). Resolve is a
// no-op when no alert is active, so the parse-wg-dump key (which never
// fired) must not produce a record.
func TestReconcile_RecoveryEmitsResolveOnce(t *testing.T) {
	m, h := newModuleForReconcileTest(t)
	failures := 0
	m.runWGShow = func(_ context.Context, _ *slog.Logger) (string, error) {
		if failures < 3 {
			failures++
			return "", errors.New("ssh: connection refused")
		}
		// Header-only output: parseWGShowDump returns an empty peer map and
		// no error, exercising the success-path Resolve calls.
		return "PRIVKEYabc=\tPUB\t51820\toff\n", nil
	}
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		if err := m.Reconcile(ctx, m.Log); err != nil {
			t.Fatalf("Reconcile #%d returned err: %v", i+1, err)
		}
	}
	records := h.snapshot()
	failureMatches := recordsMatching(records, "wg-reconcile-failed", "remote-wg-show")
	// Want exactly two: the initial ERROR transition, then the recovery
	// emit triggered by Resolve once the seam returned a parseable dump.
	if len(failureMatches) != 2 {
		t.Fatalf("got %d records for (wg-reconcile-failed, remote-wg-show), want 2 (1 emit + 1 resolve)", len(failureMatches))
	}
	parseMatches := recordsMatching(records, "wg-reconcile-failed", "parse-wg-dump")
	if len(parseMatches) != 0 {
		t.Errorf("got %d records for (wg-reconcile-failed, parse-wg-dump), want 0 (Resolve on inactive key is no-op)", len(parseMatches))
	}
	// The second remote-wg-show record is the Resolve emit. The notify
	// Manager re-emits the recovery line at the level the original Notify
	// used (ERROR here) so it crosses the same EmailConfig.MinLevel
	// threshold the open alert did; see internal/notify/manager.go
	// resolveAt. The record carries resolved=true.
	resolve := failureMatches[1]
	if resolve.Level != slog.LevelError {
		t.Errorf("resolve level=%v, want ERROR", resolve.Level)
	}
	var resolved bool
	resolve.Attrs(func(a slog.Attr) bool {
		if a.Key == "resolved" {
			resolved = a.Value.Bool()
		}
		return true
	})
	if !resolved {
		t.Errorf("resolve record missing resolved=true attribute")
	}
}

// New(nil) flags the module disabled (no [ifmgr.modules.wg] section was
// rendered). Init returns ifmgr.ErrModuleDisabled so the daemon drops
// the module from its dispatch list for its remaining lifetime.
func TestInitReturnsDisabledSentinelWhenConfigNil(t *testing.T) {
	t.Parallel()

	module, err := New(nil)
	if err != nil {
		t.Fatalf("New(nil) returned error: %v", err)
	}
	env := &ifmgr.Env{
		Iface: "lo",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(
			&config.Config{},
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			"mwan-ifmgr",
		)),
	}
	initErr := module.Init(context.Background(), env)
	if initErr == nil {
		t.Fatal("Init returned nil error after New(nil), want ErrModuleDisabled")
	}
	if !errors.Is(initErr, ifmgr.ErrModuleDisabled) {
		t.Fatalf("Init returned err=%v, want errors.Is(err, ifmgr.ErrModuleDisabled)", initErr)
	}
}

// Local-exec mode (Config{} with empty SSHHost) is a valid configuration
// on suburban and must NOT trip the disabled sentinel. The fact that
// a non-nil cfg was passed to New is the operator's signal that the
// section was rendered.
func TestInitDoesNotDisableOnDefaultLocalExecConfig(t *testing.T) {
	t.Parallel()

	module, err := New(Config{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	env := &ifmgr.Env{
		Iface: "lo",
		Log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(
			&config.Config{},
			slog.New(slog.NewTextHandler(io.Discard, nil)),
			"mwan-ifmgr",
		)),
	}
	initErr := module.Init(context.Background(), env)
	if initErr != nil {
		t.Fatalf("Init returned err=%v on default local-exec config, want nil", initErr)
	}
}
