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

	"github.com/osrg/gobgp/v4/pkg/apiutil"
	bgppkt "github.com/osrg/gobgp/v4/pkg/packet/bgp"
	"github.com/vishvananda/netlink"

	"goodkind.io/mwan/gateway/internal/bgp"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/bgpsessions"
	"goodkind.io/mwan/gateway/internal/wanstate"
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

type sweepJournal struct {
	Owners    json.RawMessage `json:"owners"`
	Retaining []struct {
		Interface string    `json:"interface"`
		Metric    int       `json:"metric"`
		Deadline  time.Time `json:"deadline"`
	} `json:"retaining"`
}

func readSweepJournal(t *testing.T, stateFile string) sweepJournal {
	t.Helper()
	data, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	var document sweepJournal
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("decode the journal %s: %v", data, err)
	}
	if len(document.Retaining) != 1 || document.Retaining[0].Interface != sweepDevice ||
		document.Retaining[0].Metric != sweepMetric {
		t.Fatalf(
			"journal = %s, want one restart deadline for %s metric %d",
			data,
			sweepDevice,
			sweepMetric,
		)
	}
	return document
}

func sweepRestartSession() bgpsessions.Session {
	return bgpsessions.Session{
		WANRef: ifmgr.WANRef{ID: "transit", Name: "transit", Iface: sweepDevice},
		Config: bgp.SessionConfig{
			Name:                   "upstream",
			RouterID:               netip.MustParseAddr("192.0.2.1"),
			Families:               []bgp.Family{bgp.FamilyIPv6Unicast},
			LocalASN:               sweepLocalASN,
			RemoteASN:              sweepPeerASN,
			PeerAddress:            netip.MustParseAddr(sweepGateway),
			PeerPort:               179,
			KeepaliveSeconds:       1,
			HoldSeconds:            3,
			GracefulRestartSeconds: sweepRestartTime,
			RouteMetric:            sweepMetric,
		},
	}
}

func TestSweepRelearnsARouteLeftByACrashedSession(t *testing.T) {
	if !runSweepIsolated(t) {
		return
	}
	link, peer := startSweepPeer(t)
	stateFile := filepath.Join(t.TempDir(), "bgp-sessions.json")
	session := sweepRestartSession()
	prefix := netip.MustParsePrefix(sweepStale)
	length := uint8(prefix.Bits())
	session.Config.Import = []bgp.ImportRule{{Prefix: prefix, MinLength: length, MaxLength: length}}
	if err := os.WriteFile(
		stateFile,
		[]byte(`{"owners":[{"interface":"`+sweepDevice+`","metric":300}]}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(sweepRoute(t, link, sweepStale)); err != nil {
		t.Fatalf("add %s: %v", sweepStale, err)
	}
	startModule(
		t,
		bgpsessions.Config{Sessions: []bgpsessions.Session{session}, StateFile: stateFile},
	)
	waitSweepPeerEstablished(t, peer)
	if sweepRoutePresent(t, link, sweepStale) {
		t.Fatalf("the restarted session kept %s without a new announcement", prefix)
	}
	nlri, err := bgppkt.NewIPAddrPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	nextHop, err := bgppkt.NewPathAttributeMpReachNLRI(
		bgppkt.RF_IPv6_UC,
		[]bgppkt.PathNLRI{{NLRI: nlri}},
		netip.MustParseAddr(sweepGateway),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.AddPath(apiutil.AddPathRequest{
		Paths: []*apiutil.Path{
			{
				Family: bgppkt.RF_IPv6_UC,
				Nlri:   nlri,
				Attrs: []bgppkt.PathAttributeInterface{
					bgppkt.NewPathAttributeOrigin(bgppkt.BGP_ORIGIN_ATTR_TYPE_IGP),
					nextHop,
				},
			},
		},
	}); err != nil {
		t.Fatalf("announce %s: %v", prefix, err)
	}
	deadline := time.Now().Add(sweepWait)
	for time.Now().Before(deadline) {
		if sweepRoutePresent(t, link, sweepStale) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the restarted session did not install %s after a new announcement", prefix)
}

func TestSweepRetainsRoutesUntilTheRestartDeadline(t *testing.T) {
	if !runSweepIsolated(t) {
		return
	}
	link, peer := startSweepPeer(t)
	stateFile := filepath.Join(t.TempDir(), "bgp-sessions.json")
	cfg := bgpsessions.Config{
		Sessions:  []bgpsessions.Session{sweepRestartSession()},
		StateFile: stateFile,
	}

	stopFirst := startModule(t, cfg)
	waitSweepPeerEstablished(t, peer)
	stopFirst()
	if deadline := readSweepJournal(t, stateFile).Retaining[0].Deadline; !deadline.After(
		time.Now(),
	) {
		t.Fatalf("shutdown journal recorded an expired deadline: %s", deadline)
	}

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

	journal := readSweepJournal(t, stateFile)
	journal.Retaining[0].Deadline = time.Now().Add(-time.Second)
	data, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stateFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	startModule(t, cfg)
	if sweepRoutePresent(t, link, sweepStale) {
		t.Fatalf("the sweep did not delete the route %s after the restart deadline", sweepStale)
	}
}
