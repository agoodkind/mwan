package wg

import (
	"context"
	"errors"
	"html"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/testsmtp"
)

type observationClock struct {
	now time.Time
}

func (clock *observationClock) Now() time.Time { return clock.now }

func emailModule(t *testing.T, cfg Config) (*Module, *testsmtp.Server) {
	t.Helper()
	server := testsmtp.Start(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := notify.New(notify.EmailConfig{
		MsmtprcPath: server.Path, AlertEmail: "recipient@example.com", SubjectPrefix: "[Suburban]", MinLevel: "ERROR",
	}, notify.Config{}, logger, "mwan-ifmgr")
	if err != nil {
		t.Fatal(err)
	}
	module, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := module.Init(t.Context(), &ifmgr.Env{Iface: "management0", Log: logger, Alerts: ifmgr.WrapNotifier(manager)}); err != nil {
		t.Fatal(err)
	}
	return module.(*Module), server
}

func requireEmailText(t *testing.T, server *testsmtp.Server, values ...string) string {
	t.Helper()
	text := html.UnescapeString(testsmtp.HTML(t, server.Next(t)))
	for _, value := range values {
		if !strings.Contains(text, value) {
			t.Fatalf("WireGuard email omitted %q:\n%s", value, text)
		}
	}
	return text
}

func TestReconcileEmailsUseCompletedCheckTimesAndLocalContext(t *testing.T) {
	t.Parallel()
	module, server := emailModule(t, Config{Iface: "wg-test"})
	clock := &observationClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
	module.clock = clock
	module.runWGShow = func(context.Context, *slog.Logger) (string, error) {
		clock.now = clock.now.Add(46 * time.Second)
		return "", errors.New("permission denied")
	}
	if err := module.Reconcile(t.Context(), module.Log); err != nil {
		t.Fatal(err)
	}
	failure := requireEmailText(t, server,
		"WireGuard status check failed for local interface wg-test.",
		"First failure observed: 2026-10-31 12:00:46 UTC (UTC+00:00)",
		"Check mode: local", "Target: local host", "Interface: wg-test", "Command: wg show wg-test dump", "permission denied")
	if strings.Contains(failure, "remote") || strings.Contains(failure, "SSH") {
		t.Fatal("local status check used remote wording")
	}
	module.runWGShow = func(context.Context, *slog.Logger) (string, error) {
		clock.now = clock.now.Add(10 * time.Second)
		return "header\nmalformed peer\n", nil
	}
	if err := module.Reconcile(t.Context(), module.Log); err != nil {
		t.Fatal(err)
	}
	requireEmailText(t, server, "WireGuard status check returned an invalid peer table for local interface wg-test.")
	if !module.Env.Alerts.Active("wg-reconcile-failed", "remote-wg-show") {
		t.Fatal("command success cleared the incident before parsing succeeded")
	}
	module.runWGShow = func(context.Context, *slog.Logger) (string, error) {
		clock.now = clock.now.Add(55 * time.Second)
		return sampleDump, nil
	}
	if err := module.Reconcile(t.Context(), module.Log); err != nil {
		t.Fatal(err)
	}
	recovery := requireEmailText(t, server,
		"Recovered: WireGuard status check succeeded for local interface wg-test.",
		"Recovery observed: 2026-10-31 12:01:51 UTC (UTC+00:00)",
		"Observed duration: 1 minute, 5 seconds", "permission denied")
	previous := recovery[strings.LastIndex(recovery, "Last failed observation:"):]
	for _, value := range []string{"Interface: wg-test", "Target: local host", "Command: wg show wg-test dump"} {
		if strings.Contains(previous, value) {
			t.Fatalf("recovery duplicated context %q:\n%s", value, recovery)
		}
	}
	requireEmailText(t, server, "Recovered: WireGuard status check succeeded", "want 8 tab-separated fields")
	if module.Env.Alerts.Active("wg-reconcile-failed", "remote-wg-show") || module.Env.Alerts.Active("wg-reconcile-failed", "parse-wg-dump") {
		t.Fatal("valid peer table did not clear the check incidents")
	}
}

func TestReconcileEmailReportsActualSSHCommandTimeout(t *testing.T) {
	t.Parallel()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		connection, acceptError := listener.Accept()
		if acceptError != nil {
			return
		}
		defer connection.Close()
		_, _ = io.Copy(io.Discard, connection)
	}()
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	module, server := emailModule(t, Config{Iface: "wg-ssh", SSHHost: "127.0.0.1", SSHPort: port, Timeout: time.Second})
	if err := module.Reconcile(t.Context(), module.Log); err != nil {
		t.Fatal(err)
	}
	text := requireEmailText(t, server, "WireGuard status check timed out for interface wg-ssh on SSH target 127.0.0.1.", "context deadline exceeded", "Check mode: SSH", "Interface: wg-ssh", "Target: 127.0.0.1")
	if strings.Contains(text, "tunnel failed") || strings.Contains(text, "peer handshake") {
		t.Fatal("command timeout claimed a tunnel failure")
	}
}

func TestPeerEmailsIdentifyWireGuardAndHandshake(t *testing.T) {
	t.Parallel()
	module, server := emailModule(t, Config{Iface: "wg-peers", SSHHost: "operator@router", WarnHandshakeAge: 3 * time.Minute, ErrorHandshakeAge: 5 * time.Minute})
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	module.lastRunAt = now
	module.lastPeers = map[string]peerState{"peer-key": {endpoint: "192.0.2.1:51820", handshake: now.Add(-6 * time.Minute)}}
	module.EvaluateAlerts(t.Context(), module.Log, now)
	requireEmailText(t, server, "WireGuard peer peer-key exceeded the handshake error threshold", "Interface: wg-peers", "Target: operator@router", "Endpoint: 192.0.2.1:51820", "Handshake age (seconds): 360", "Threshold (seconds): 300", "Last handshake: 2026-10-31 11:54:00 UTC (UTC+00:00)")
	module.lastPeers = map[string]peerState{"peer-key": {endpoint: "192.0.2.1:51820", handshake: now.Add(time.Minute)}}
	module.EvaluateAlerts(t.Context(), module.Log, now.Add(2*time.Minute))
	requireEmailText(t, server, "Recovered: WireGuard peer peer-key completed a recent handshake", "Handshake age (seconds): 60", "Handshake age (seconds): 360")
}
