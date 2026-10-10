//go:build linux && netns

package netif

import (
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

const (
	tunnelChildEnv      = "MWAN_OWNED_TUNNEL_CHILD"
	tunnelName          = "mwan6in4"
	tunnelUnderlayName  = "tun-under"
	tunnelLocalOuter    = "192.0.2.1"
	tunnelRemoteOuter   = "192.0.2.2"
	tunnelLocalInner    = "2001:db8:41::1"
	tunnelRemoteInner   = "2001:db8:41::2"
	tunnelCaptureWindow = 5 * time.Second
)

func enterTunnelNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(tunnelChildEnv) == t.Name() {
		return true
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespace requires root")
	}
	child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), tunnelChildEnv+"="+t.Name())
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated tunnel test: %v: %s", err, output)
	}
	return false
}

func tunnelTestLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func addTunnelUnderlay(t *testing.T, name string) netlink.Link {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}); err != nil {
		t.Fatalf("create underlay %s: %v", name, err)
	}
	underlay, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(underlay); err != nil {
		t.Fatal(err)
	}
	return underlay
}

func externalConnection(name string) interfaceintent.Connection {
	return interfaceintent.Connection{ID: connectionid.ID(name), Name: name, Owner: interfaceintent.OwnerExternal}
}

func tunnelConnection(underlay string, remote string, ttl *uint8, mtu *uint32) interfaceintent.Connection {
	return namedTunnelConnection(tunnelName, underlay, remote, ttl, mtu)
}

func namedTunnelConnection(name string, underlay string, remote string, ttl *uint8, mtu *uint32) interfaceintent.Connection {
	return interfaceintent.Connection{
		ID: connectionid.ID(name), Name: name, Owner: interfaceintent.OwnerMWAN,
		Link: &interfaceintent.Link{
			Kind: interfaceintent.KindTunnel, MTU: mtu,
			Tunnel: &interfaceintent.Tunnel{
				Protocol: interfaceintent.TunnelProtocol6in4, Underlay: underlay,
				Remote: netip.MustParseAddr(remote), Local: netip.MustParseAddr(tunnelLocalOuter), TTL: ttl,
			},
		},
	}
}

func newTunnelReconciler(t *testing.T, statePath string) *OwnedLinkReconciler {
	t.Helper()
	reconciler, err := NewOwnedLinkReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func reconcileTunnel(t *testing.T, reconciler *OwnedLinkReconciler, connections ...interfaceintent.Connection) OwnedLinkResult {
	t.Helper()
	results, err := reconciler.Reconcile(t.Context(), tunnelTestLog(), connections)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	for _, result := range results {
		if result.Name == tunnelName {
			return result
		}
	}
	t.Fatalf("no result for %s in %+v", tunnelName, results)
	return OwnedLinkResult{}
}

func requireTunnelReady(t *testing.T, result OwnedLinkResult) {
	t.Helper()
	if result.Status != OwnedLinkReady || result.Err != nil {
		t.Fatalf("tunnel result = %+v", result)
	}
}

func tunnelDevice(t *testing.T, name string) *netlink.Sittun {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	device, ok := link.(*netlink.Sittun)
	if !ok {
		t.Fatalf("%s has kernel type %s, want sit", name, link.Type())
	}
	return device
}

func requireTunnelAttributes(t *testing.T, device *netlink.Sittun, underlay netlink.Link, remote string, ttl uint8, mtu int) {
	t.Helper()
	if !device.Remote.Equal(net.ParseIP(remote)) || !device.Local.Equal(net.ParseIP(tunnelLocalOuter)) {
		t.Fatalf("tunnel endpoints local=%s remote=%s, want local=%s remote=%s",
			device.Local, device.Remote, tunnelLocalOuter, remote)
	}
	if device.Ttl != ttl {
		t.Fatalf("tunnel TTL = %d, want %d", device.Ttl, ttl)
	}
	if device.Attrs().MTU != mtu {
		t.Fatalf("tunnel MTU = %d, want %d", device.Attrs().MTU, mtu)
	}
	if device.Attrs().ParentIndex != underlay.Attrs().Index {
		t.Fatalf("tunnel underlay index = %d, want %d (%s)",
			device.Attrs().ParentIndex, underlay.Attrs().Index, underlay.Attrs().Name)
	}
	if device.Proto != unix.IPPROTO_IPV6 {
		t.Fatalf("tunnel inner protocol = %d, want %d", device.Proto, unix.IPPROTO_IPV6)
	}
	if device.Attrs().Flags&net.FlagUp == 0 {
		t.Fatal("tunnel is administratively down")
	}
}

func mustTunnelAddress(t *testing.T, cidr string) *netlink.Addr {
	t.Helper()
	address, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	if address.IP.To4() == nil {
		// The exchange starts immediately. A tentative IPv6 address rejects connections.
		address.Flags = unix.IFA_F_NODAD
	}
	return address
}
