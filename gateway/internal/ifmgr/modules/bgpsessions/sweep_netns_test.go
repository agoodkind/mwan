//go:build linux && netns

package bgpsessions_test

import (
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

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
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	module, err := bgpsessions.New(bgpsessions.Config{
		StateFile: stateFile,
		ConfiguredRoutes: []bgpsessions.ConfiguredRoute{
			{Interface: sweepDevice, Dest: sweepConfigured, Via: sweepGateway, Metric: sweepMetric},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.Init(t.Context(), &ifmgr.Env{Log: log, LiveState: wanstate.New()}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := module.Reconcile(t.Context(), log); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

func TestSweepKeepsConfiguredRoutesOfARemovedSession(t *testing.T) {
	if os.Getenv(sweepChildEnv) != "1" {
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: uintptr(unix.CLONE_NEWNET)}
		child.Env = append(os.Environ(), sweepChildEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated sweep test: %v: %s", err, output)
		}
		return
	}
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
