//go:build linux && netns

package links_test

import (
	"context"
	"io"
	"log/slog"
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
)

// awaitUnderlayEvent consumes reconcile requests until the underlay monitor
// has requested one and no further request arrives within the quiet period.
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

func TestLinksModuleCreatesTunnelAfterUnderlayAppears(t *testing.T) {
	if os.Getenv(tunnelChildEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestLinksModuleCreatesTunnelAfterUnderlayAppears$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), tunnelChildEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated links module test: %v: %s", err, output)
		}
		return
	}
	connections := []interfaceintent.Connection{
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
	module, err := links.New(links.Config{Connections: connections, StateFile: filepath.Join(t.TempDir(), "links.json")})
	if err != nil {
		t.Fatal(err)
	}
	store := wanstate.New()
	store.SetConnections(connections)
	requests := make(chan string, 64)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	env := &ifmgr.Env{
		Connections: connections, Log: log, LiveState: store,
		RequestReconcile: func(reason string) {
			select {
			case requests <- reason:
			default:
			}
		},
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if err := module.Init(ctx, env); err != nil {
		t.Fatal(err)
	}

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
