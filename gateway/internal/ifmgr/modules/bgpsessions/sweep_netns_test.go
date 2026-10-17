//go:build linux && netns

package bgpsessions_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/bgp"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/bgpsessions"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	sweepChildEnv   = "MWAN_BGP_SWEEP_TEST_CHILD"
	sweepDevice     = "enold0"
	sweepMetric     = 300
	sweepGateway    = "2001:db8:1::1"
	sweepConfigured = "2001:db8:c0::/48"
	sweepStale      = "2001:db8:5a::/48"
	sweepLater      = "2001:db8:5b::/48"
)

func sweepRoute(t *testing.T, link netlink.Link, destination string) *netlink.Route {
	t.Helper()
	_, network, err := net.ParseCIDR(destination)
	if err != nil {
		t.Fatal(err)
	}
	return &netlink.Route{
		LinkIndex: link.Attrs().Index, Dst: network, Gw: net.ParseIP(sweepGateway),
		Priority: sweepMetric, Protocol: unix.RTPROT_BGP, Table: unix.RT_TABLE_MAIN,
	}
}

func sweepRoutePresent(t *testing.T, link netlink.Link, destination string) bool {
	t.Helper()
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_V6, sweepRoute(t, link, destination), netlink.RT_FILTER_DST|netlink.RT_FILTER_OIF)
	if err != nil {
		t.Fatal(err)
	}
	return len(routes) == 1
}

func reconcileSweepModule(t *testing.T, stateFile string) {
	t.Helper()
	reconcileModule(t, bgpsessions.Config{
		StateFile: stateFile,
		ConfiguredRoutes: []bgpsessions.ConfiguredRoute{
			{Interface: sweepDevice, Dest: sweepConfigured, Via: sweepGateway, Metric: sweepMetric},
		},
	})
}

// reconcileModule runs Init and one Reconcile, then stops the module at the end
// of the test.
func reconcileModule(t *testing.T, cfg bgpsessions.Config) {
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
	t.Cleanup(func() {
		cancel()
		module.(*bgpsessions.Module).Wait()
	})
	if err := module.Reconcile(ctx, log); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// runSweepIsolated reports true in the child process, which runs in a new
// network namespace.
func runSweepIsolated(t *testing.T) bool {
	t.Helper()
	if os.Getenv(sweepChildEnv) == "1" {
		return true
	}
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET)}
	child.Env = append(os.Environ(), sweepChildEnv+"=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated sweep test: %v: %s", err, output)
	}
	return false
}

func TestSweepKeepsRoutesUntilTheRestartDeadline(t *testing.T) {
	if !runSweepIsolated(t) {
		return
	}
	link := addSweepLink(t)
	session := bgpsessions.Session{
		WANRef: ifmgr.WANRef{ID: "transit", Name: "transit", Iface: sweepDevice},
		Config: bgp.SessionConfig{
			Name: "upstream", RouterID: netip.MustParseAddr("192.0.2.1"), LocalASN: 64512, RemoteASN: 64513,
			PeerAddress: netip.MustParseAddr(sweepGateway), PeerPort: 179, KeepaliveSeconds: 1, HoldSeconds: 3,
			GracefulRestartSeconds: 60, RouteMetric: sweepMetric,
		},
	}
	stateFile := filepath.Join(t.TempDir(), "bgp-sessions.json")
	writeJournal := func(deadline time.Time) {
		journal := fmt.Sprintf(`{"owners":[{"interface":%q,"metric":%d}],"retaining":[{"interface":%q,"metric":%d,"deadline":%q}]}`,
			sweepDevice, sweepMetric, sweepDevice, sweepMetric, deadline.Format(time.RFC3339Nano))
		if err := os.WriteFile(stateFile, []byte(journal), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.RouteAdd(sweepRoute(t, link, sweepStale)); err != nil {
		t.Fatalf("add %s: %v", sweepStale, err)
	}
	cfg := bgpsessions.Config{Sessions: []bgpsessions.Session{session}, StateFile: stateFile}

	writeJournal(time.Now().Add(time.Minute))
	reconcileModule(t, cfg)
	if !sweepRoutePresent(t, link, sweepStale) {
		t.Fatalf("the sweep deleted the kept route %s before the restart deadline", sweepStale)
	}

	writeJournal(time.Now().Add(-time.Second))
	reconcileModule(t, cfg)
	if sweepRoutePresent(t, link, sweepStale) {
		t.Fatalf("the sweep kept the route %s after the restart deadline", sweepStale)
	}
}

func TestSweepKeepsConfiguredRoutesOfARemovedSession(t *testing.T) {
	if !runSweepIsolated(t) {
		return
	}
	link := addSweepLink(t)
	stateFile := filepath.Join(t.TempDir(), "bgp-sessions.json")
	journal := `{"owners":[{"interface":"` + sweepDevice + `","metric":300}]}`
	if err := os.WriteFile(stateFile, []byte(journal), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{sweepConfigured, sweepStale} {
		if err := netlink.RouteAdd(sweepRoute(t, link, destination)); err != nil {
			t.Fatalf("add %s: %v", destination, err)
		}
	}

	reconcileSweepModule(t, stateFile)
	if !sweepRoutePresent(t, link, sweepConfigured) {
		t.Fatalf("the sweep deleted the configured route %s", sweepConfigured)
	}
	if sweepRoutePresent(t, link, sweepStale) {
		t.Fatalf("the sweep did not delete the route %s of the removed session", sweepStale)
	}

	if err := netlink.RouteAdd(sweepRoute(t, link, sweepLater)); err != nil {
		t.Fatalf("add %s: %v", sweepLater, err)
	}
	reconcileSweepModule(t, stateFile)
	if !sweepRoutePresent(t, link, sweepLater) || !sweepRoutePresent(t, link, sweepConfigured) {
		t.Fatal("the second process deleted a route after the first process dropped the journal record")
	}
}

func addSweepLink(t *testing.T) netlink.Link {
	t.Helper()
	attributes := netlink.NewLinkAttrs()
	attributes.Name = sweepDevice
	link := netlink.Link(&netlink.Dummy{LinkAttrs: attributes})
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr("2001:db8:1::2/64")
	if err != nil {
		t.Fatal(err)
	}
	address.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	link, err = netlink.LinkByName(sweepDevice)
	if err != nil {
		t.Fatal(err)
	}
	return link
}
