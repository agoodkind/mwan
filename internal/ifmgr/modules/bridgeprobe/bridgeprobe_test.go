package bridgeprobe

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
)

func TestSnapshotReplayDoesNotRefreshLastRA(t *testing.T) {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	notifier, err := notify.New(nil, log, "bridge-probe-test")
	if err != nil {
		t.Fatal(err)
	}
	alerts := ifmgr.WrapNotifier(notifier)
	m := &Module{
		cfg:   Config{Iface: "bridge0", NoSignalAlertAfter: 5 * time.Millisecond},
		clock: internalclock.Real{},
	}
	m.Env = &ifmgr.Env{Alerts: alerts}
	for _, event := range []netif.Event{
		{Kind: netif.EvLinkUp, Iface: "bridge0"},
		{Kind: netif.EvRouteAdded, Iface: "bridge0", Family: "inet6", Dest: "default"},
	} {
		if err := m.OnKernelEvent(ctx, log, event); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.OnDHCPLease(ctx, log, netif.LeaseInfo{State: netif.LeaseBound}); err != nil {
		t.Fatal(err)
	}
	alerts.NotifyContext(ctx, time.Now(), slog.LevelWarn, "slaac-degraded", "bridge0", "SLAAC unavailable")
	time.Sleep(20 * time.Millisecond)
	if err := m.OnKernelEvent(ctx, log, netif.Event{
		Kind:           netif.EvRouteAdded,
		Iface:          "bridge0",
		Family:         "inet6",
		Dest:           "default",
		SnapshotReplay: true,
	}); err != nil {
		t.Fatal(err)
	}
	m.EvaluateAlerts(ctx, log, time.Now())
	if !alerts.Active("bridge-suspected-dangling", "bridge0") {
		t.Fatal("snapshot replay suppressed the missing-signal alert")
	}
}
