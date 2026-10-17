//go:build linux && netns

package bgpsessions_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/vishvananda/netlink"

	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/bgpsessions"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	sweepPeerDevice   = "sweeppeer0"
	sweepLocalAddress = "2001:db8:1::2"
	sweepLocalASN     = 64512
	sweepPeerASN      = 64513
	sweepRestartTime  = 8
	sweepWait         = 30 * time.Second
)

// startModule runs Init and one Reconcile. The returned function cancels the
// module context and waits for the shutdown path.
func startModule(t *testing.T, cfg bgpsessions.Config) func() {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	module, err := bgpsessions.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if err := module.Init(ctx, &ifmgr.Env{Log: log, LiveState: wanstate.New()}); err != nil {
		cancel()
		t.Fatalf("Init: %v", err)
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			module.(*bgpsessions.Module).Wait()
		})
	}
	t.Cleanup(stop)
	if err := module.Reconcile(ctx, log); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return stop
}

// journalDeadline reads the restart deadline that the module shutdown path
// wrote for the session owner.
func journalDeadline(t *testing.T, stateFile string) time.Time {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Retaining []struct {
			Interface string    `json:"interface"`
			Metric    int       `json:"metric"`
			Deadline  time.Time `json:"deadline"`
		} `json:"retaining"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode the journal %s: %v", data, err)
	}
	if len(document.Retaining) != 1 || document.Retaining[0].Interface != sweepDevice || document.Retaining[0].Metric != sweepMetric {
		t.Fatalf("journal = %s, want one restart deadline for %s metric %d", data, sweepDevice, sweepMetric)
	}
	return document.Retaining[0].Deadline
}

func TestSweepRetainsRoutesUntilTheRestartDeadline(t *testing.T) {
	if !runSweepIsolated(t) {
		return
	}
	link, peer := startSweepPeer(t)
	session := bgpsessions.Session{
		WANRef: ifmgr.WANRef{ID: "transit", Name: "transit", Iface: sweepDevice},
		Config: bgp.SessionConfig{
			Name: "upstream", RouterID: netip.MustParseAddr("192.0.2.1"), LocalASN: sweepLocalASN, RemoteASN: sweepPeerASN,
			PeerAddress: netip.MustParseAddr(sweepGateway), PeerPort: 179, KeepaliveSeconds: 1, HoldSeconds: 3,
			GracefulRestartSeconds: sweepRestartTime, RouteMetric: sweepMetric,
		},
	}
	stateFile := filepath.Join(t.TempDir(), "bgp-sessions.json")
	cfg := bgpsessions.Config{Sessions: []bgpsessions.Session{session}, StateFile: stateFile}

	stopFirst := startModule(t, cfg)
	waitSweepPeerEstablished(t, peer)
	stopFirst()
	deadline := journalDeadline(t, stateFile)

	if err := netlink.RouteAdd(sweepRoute(t, link, sweepStale)); err != nil {
		t.Fatalf("add %s: %v", sweepStale, err)
	}
	stopSecond := startModule(t, cfg)
	if !sweepRoutePresent(t, link, sweepStale) {
		t.Fatalf("the sweep deleted the retained route %s before the restart deadline", sweepStale)
	}
	stopSecond()
	if !sweepRoutePresent(t, link, sweepStale) {
		t.Fatalf("the second module shutdown deleted the retained route %s", sweepStale)
	}

	// The second shutdown writes a later deadline when its session
	// established.
	if second := journalDeadline(t, stateFile); second.After(deadline) {
		deadline = second
	}
	time.Sleep(time.Until(deadline) + time.Second)
	startModule(t, cfg)
	if sweepRoutePresent(t, link, sweepStale) {
		t.Fatalf("the sweep did not delete the route %s after the restart deadline", sweepStale)
	}
}
