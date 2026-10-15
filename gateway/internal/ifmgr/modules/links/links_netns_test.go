//go:build linux && netns

package links_test

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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/links"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const (
	tunnelChildEnv     = "MWAN_LINKS_TUNNEL_CHILD"
	tunnelID           = "tunnel-6in4"
	tunnelName         = "mwan6in4"
	underlayName       = "tun-under"
	underlayEvent      = "link event on " + underlayName
	underlayEventLimit = 10 * time.Second
	underlayEventQuiet = 500 * time.Millisecond
	underlayRoute      = "192.0.2.0/24"
	underlayLocalRoute = "203.0.113.0/24"
	policyTable        = 700
)

// runInNetns reports whether the caller is the isolated child that runs the test body.
func runInNetns(t *testing.T, testName string) bool {
	t.Helper()
	if os.Getenv(tunnelChildEnv) == "1" {
		return true
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespace requires root")
	}
	child := exec.Command(os.Args[0], "-test.run=^"+testName+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), tunnelChildEnv+"=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated links module test: %v: %s", err, output)
	}
	return false
}

func tunnelConnections() []interfaceintent.Connection {
	return []interfaceintent.Connection{
		{ID: underlayName, Name: underlayName, Owner: interfaceintent.OwnerExternal},
		{
			ID: tunnelID, Name: tunnelName, Owner: interfaceintent.OwnerMWAN,
			Link: &interfaceintent.Link{
				Kind: interfaceintent.KindTunnel,
				Tunnel: &interfaceintent.Tunnel{
					Protocol: interfaceintent.TunnelProtocol6in4, Underlay: underlayName,
					Remote: netip.MustParseAddr("198.51.100.1"),
				},
			},
		},
	}
}

// startLinksModule initializes the module and returns the reconcile reasons it requests.
func startLinksModule(
	ctx context.Context, t *testing.T, connections []interfaceintent.Connection,
) (ifmgr.Module, *ifmgr.Env, *wanstate.Store, <-chan string) {
	t.Helper()
	module, err := links.New(links.Config{Connections: connections, StateFile: filepath.Join(t.TempDir(), "links.json")})
	if err != nil {
		t.Fatal(err)
	}
	store := wanstate.New()
	store.SetConnections(connections)
	requests := make(chan string, 64)
	env := &ifmgr.Env{
		Connections: connections, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), LiveState: store,
		RequestReconcile: func(reason string) {
			select {
			case requests <- reason:
			default:
			}
		},
	}
	if err := module.Init(ctx, env); err != nil {
		t.Fatal(err)
	}
	return module, env, store, requests
}

func awaitUnderlayEvent(t *testing.T, requests <-chan string) {
	t.Helper()
	limit := time.After(underlayEventLimit)
	requested := false
	for {
		var quiet <-chan time.Time
		if requested {
			quiet = time.After(underlayEventQuiet)
		}
		select {
		case reason := <-requests:
			requested = requested || reason == underlayEvent
		case <-quiet:
			return
		case <-limit:
			t.Fatalf("no reconcile request with reason %q", underlayEvent)
		}
	}
}

func awaitRouteRequest(t *testing.T, requests <-chan string, table int) {
	t.Helper()
	want := fmt.Sprintf("route event in table %d on %s", table, underlayName)
	limit := time.After(underlayEventLimit)
	for {
		select {
		case reason := <-requests:
			if reason == want {
				return
			}
		case <-limit:
			t.Fatalf("no reconcile request with reason %q", want)
		}
	}
}

// A request left from an earlier route would satisfy the next route assertion.
func drainRequests(requests <-chan string) {
	for {
		select {
		case <-requests:
		case <-time.After(underlayEventQuiet):
			return
		}
	}
}

func TestLinksModuleCreatesTunnelAfterUnderlayAppears(t *testing.T) {
	if !runInNetns(t, "TestLinksModuleCreatesTunnelAfterUnderlayAppears") {
		return
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	module, env, store, requests := startLinksModule(ctx, t, tunnelConnections())
	log := env.Log

	if err := module.Reconcile(ctx, log); err != nil {
		t.Fatal(err)
	}

	apply := store.Snapshot().Connections[tunnelID].LastApply
	if apply.Result != string(netif.OwnedLinkWaiting) || apply.Operation != "wait-for-underlay" ||
		apply.Dependency != underlayName || !strings.Contains(apply.Reason, "underlay "+underlayName) {
		t.Fatalf("published apply result without the underlay = %+v", apply)
	}
	// The underlay monitor reports its first observation before the device exists.
	awaitUnderlayEvent(t, requests)

	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: underlayName}}); err != nil {
		t.Fatal(err)
	}
	awaitUnderlayEvent(t, requests)
	if err := module.Reconcile(ctx, log); err != nil {
		t.Fatal(err)
	}

	apply = store.Snapshot().Connections[tunnelID].LastApply
	if apply.Result != string(netif.OwnedLinkReady) {
		t.Fatalf("published apply result with the underlay = %+v", apply)
	}
	device, err := netlink.LinkByName(tunnelName)
	if err != nil || device.Type() != "sit" {
		t.Fatalf("tunnel device after the underlay appeared: link=%+v err=%v", device, err)
	}
	result, published := env.OwnedLinks.Get(tunnelID)
	if !published || result.Status != netif.OwnedLinkReady || result.IfIndex != device.Attrs().Index {
		t.Fatalf("shared link result = %+v published=%t, device index = %d", result, published, device.Attrs().Index)
	}
}

func TestLinksModuleRequestsReconcileForMainTableRoutesButNotPolicyTableRoutes(t *testing.T) {
	if !runInNetns(t, "TestLinksModuleRequestsReconcileForMainTableRoutesButNotPolicyTableRoutes") {
		return
	}
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: underlayName}}); err != nil {
		t.Fatal(err)
	}
	underlay, err := netlink.LinkByName(underlayName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(underlay); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, _, _, requests := startLinksModule(ctx, t, tunnelConnections())
	awaitUnderlayEvent(t, requests)

	_, destination, err := net.ParseCIDR(underlayRoute)
	if err != nil {
		t.Fatal(err)
	}
	// This route represents the repeated policy-table writes that caused the loop.
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: underlay.Attrs().Index, Dst: destination, Scope: netlink.SCOPE_LINK, Table: policyTable,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case reason := <-requests:
		t.Fatalf("route %s in table %d on %s requested a reconcile: %q", underlayRoute, policyTable, underlayName, reason)
	case <-time.After(underlayEventQuiet):
	}

	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: underlay.Attrs().Index, Dst: destination, Scope: netlink.SCOPE_LINK, Table: unix.RT_TABLE_MAIN,
	}); err != nil {
		t.Fatal(err)
	}
	awaitRouteRequest(t, requests, unix.RT_TABLE_MAIN)
	drainRequests(requests)

	_, localDestination, err := net.ParseCIDR(underlayLocalRoute)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: underlay.Attrs().Index, Dst: localDestination, Scope: netlink.SCOPE_LINK, Table: unix.RT_TABLE_LOCAL,
	}); err != nil {
		t.Fatal(err)
	}
	awaitRouteRequest(t, requests, unix.RT_TABLE_LOCAL)

	if err := netlink.LinkSetDown(underlay); err != nil {
		t.Fatal(err)
	}
	awaitUnderlayEvent(t, requests)
}
