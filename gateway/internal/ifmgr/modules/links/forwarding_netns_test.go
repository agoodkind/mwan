//go:build linux && netns

package links_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/autoconfiguration"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

func TestLinksModuleFencesKernelWritesUntilRetirementSucceeds(t *testing.T) {
	if !runInNetns(t, "TestLinksModuleFencesKernelWritesUntilRetirementSucceeds") {
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
	connections := tunnelConnections()
	forwarding := true
	connections[1].IPv4 = &interfaceintent.IPv4{Family: interfaceintent.Family{Forwarding: &forwarding}}
	module, env, store, _ := startLinksModule(ctx, t, connections)
	changes := new(ifmgr.ForwardingChanges)
	env.ForwardingChanges = changes
	env.Sysctl = netif.NewProcSysctlRunner(env.Log, false)
	policy, err := autoconfiguration.New(autoconfiguration.Config{
		Connections: connections, StateFile: filepath.Join(t.TempDir(), "kernel-policy.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Init(ctx, env); err != nil {
		t.Fatal(err)
	}
	receipt, err := os.Create(filepath.Join(t.TempDir(), "retirement-receipt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.Close(); err != nil {
		t.Fatal(err)
	}
	unregister, err := changes.Register(func(context.Context) (func(), error) {
		return nil, receipt.Sync()
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := module.Reconcile(ctx, env.Log); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("link write after retirement failed returned %v", err)
	}
	if _, err := netlink.LinkByName(tunnelName); !netif.IsLinkNotFound(err) {
		t.Fatalf("link appeared before retirement succeeded: %v", err)
	}
	unregister()
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	unregister, err = changes.Register(func(context.Context) (func(), error) {
		if err := listener.Close(); err != nil {
			return nil, err
		}
		return func() {
			result, ok := env.OwnedLinks.Get(tunnelID)
			if !ok || result.Status != netif.OwnedLinkReady {
				t.Errorf("guard completion preceded link publication: %+v, published=%t", result, ok)
			}
			if result := store.Snapshot().Connections[tunnelID].LastApply; result.Result != string(netif.OwnedLinkReady) {
				t.Errorf("guard completion preceded apply publication: %+v", result)
			}
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unregister)
	if err := module.Reconcile(ctx, env.Log); err != nil {
		t.Fatal(err)
	}
	device, err := netlink.LinkByName(tunnelName)
	if err != nil || device.Type() != "sit" {
		t.Fatalf("kernel link after successful retirement: link=%+v error=%v", device, err)
	}
	key := "net.ipv4.conf." + tunnelName + ".forwarding"
	if value, err := env.Sysctl.Get(ctx, key); err != nil || value != "1" {
		t.Fatalf("nested link preparation did not apply forwarding: value=%q error=%v", value, err)
	}
	ready, ok := env.OwnedLinks.Get(tunnelID)
	if !ok {
		t.Fatal("link result is absent after successful retirement")
	}
	disabled := false
	changed := connections[1]
	changed.IPv4 = &interfaceintent.IPv4{Family: interfaceintent.Family{Forwarding: &disabled}}
	if err := env.PrepareOwnedLink(ctx, env.Log, changed, ready); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("independent policy writer bypassed failed retirement: %v", err)
	}
	if value, err := env.Sysctl.Get(ctx, key); err != nil || value != "1" {
		t.Fatalf("failed retirement changed kernel forwarding: value=%q error=%v", value, err)
	}
}
