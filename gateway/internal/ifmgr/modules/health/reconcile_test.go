package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

func TestReconcileRequestReadsCommittedConnectionHealth(t *testing.T) {
	t.Parallel()

	const connectionID = "uplink-1"

	var probeErr error
	probe := func(context.Context, string, netip.Addr, time.Duration) (time.Duration, error) {
		return 0, probeErr
	}
	module := testProbeModule(probe, probe)
	module.cfg.StateFile = filepath.Join(t.TempDir(), "health.state")
	module.cfg.Interval = time.Hour
	module.cfg.FailureThreshold = 2
	module.cfg.RecoveryThreshold = 2
	module.cfg.WANs[0].ID = connectionID

	store := wanstate.New()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var verdicts []wanstate.Health
	env := &ifmgr.Env{
		Log:       log,
		LiveState: store,
		RequestReconcile: func(string) {
			verdicts = append(verdicts, store.Snapshot().Health[connectionID].Verdict)
		},
	}
	if err := module.Init(t.Context(), env); err != nil {
		t.Fatal(err)
	}

	unavailable := errors.New("probe target unavailable")
	for _, cycle := range []struct {
		probeErr error
		wantWake bool
		verdict  wanstate.Health
	}{
		{nil, true, wanstate.HealthHealthy},
		{unavailable, false, wanstate.HealthHealthy},
		{unavailable, true, wanstate.HealthUnhealthy},
		{nil, false, wanstate.HealthUnhealthy},
		{nil, true, wanstate.HealthHealthy},
	} {
		probeErr = cycle.probeErr
		verdicts = nil
		if err := module.runCycle(t.Context(), log); err != nil {
			t.Fatal(err)
		}

		if (len(verdicts) > 0) != cycle.wantWake {
			t.Fatalf(
				"probe error %v: reconcile requests %v, want wake %t",
				probeErr,
				verdicts,
				cycle.wantWake,
			)
		}

		for _, verdict := range verdicts {
			if verdict != cycle.verdict {
				t.Fatalf("reconcile read %q for %s, want %q", verdict, connectionID, cycle.verdict)
			}
		}
	}
}
