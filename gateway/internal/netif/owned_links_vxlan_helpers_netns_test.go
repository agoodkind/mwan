//go:build linux && netns

package netif

import (
	"os"
	"os/exec"
	"regexp"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	clockpkg "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/interfaceintent"
)

const (
	vxlanChildEnv        = "MWAN_OWNED_VXLAN_CHILD"
	vxlanName            = "mwan-vxlan"
	vxlanUnderlayName    = "vx-under"
	vxlanSourcePortMin   = 49152
	vxlanSourcePortMax   = 49153
	vxlanDestinationPort = 4789
	vxlanOuterTOS        = 32
	vxlanOuterTTL        = 48
)

func enterVXLANNamespace(t *testing.T) bool {
	t.Helper()
	if os.Getenv(vxlanChildEnv) == t.Name() {
		return true
	}
	if os.Geteuid() != 0 {
		t.Fatal("owned VXLAN tests require a privileged network namespace")
	}
	deadline, bounded := t.Deadline()
	if !bounded {
		t.Fatal("owned VXLAN tests require a test deadline")
	}
	remaining := deadline.Sub(clockpkg.Real{}.Now())
	if remaining <= 0 {
		t.Fatal("VXLAN namespace started after the test deadline")
	}
	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^"+regexp.QuoteMeta(t.Name())+"$", "-test.timeout="+remaining.String(), "-test.v")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), vxlanChildEnv+"="+t.Name())
	output, err := child.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated VXLAN test: %v: %s", err, output)
	}
	t.Log(string(output))
	return false
}

func vxlanConnection() interfaceintent.Connection {
	connection := namedTunnelConnection(vxlanName, vxlanUnderlayName, tunnelRemoteOuter, new(uint8(vxlanOuterTTL)), nil)
	connection.Link.Tunnel.Protocol = interfaceintent.TunnelProtocolVXLAN
	connection.Link.Tunnel.VXLAN = &interfaceintent.VXLAN{
		VNI: 0, SourcePortMin: vxlanSourcePortMin, SourcePortMax: vxlanSourcePortMax, DestinationPort: vxlanDestinationPort, TOS: vxlanOuterTOS,
	}
	return connection
}

func reconcileVXLAN(t *testing.T, reconciler *OwnedLinkReconciler, connection interfaceintent.Connection, underlays ...interfaceintent.Connection) OwnedLinkResult {
	t.Helper()
	results, err := reconciler.Reconcile(t.Context(), tunnelTestLog(), append(underlays, connection), nil)
	if err != nil {
		t.Fatalf("reconcile VXLAN links: %v", err)
	}
	for _, result := range results {
		if result.ConnectionID == connection.ID.String() {
			return result
		}
	}
	t.Fatalf("VXLAN result omitted connection %s: %+v", connection.ID, results)
	return OwnedLinkResult{}
}

func vxlanDevice(t *testing.T, name string) *netlink.Vxlan {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	device, ok := link.(*netlink.Vxlan)
	if !ok {
		t.Fatalf("%s has type %s, expected vxlan", name, link.Type())
	}
	return device
}

func requireVXLANAttributes(t *testing.T, device *netlink.Vxlan, connection interfaceintent.Connection, underlay netlink.Link) {
	t.Helper()
	tunnel := connection.Link.Tunnel
	settings := tunnel.VXLAN
	if device.VxlanId != int(settings.VNI) || device.VtepDevIndex != underlay.Attrs().Index ||
		device.Port != int(settings.DestinationPort) || device.PortLow != int(settings.SourcePortMin) || device.PortHigh != int(settings.SourcePortMax) ||
		device.TTL != int(*tunnel.TTL) || device.TOS != int(settings.TOS) || device.Learning || device.MasterIndex != 0 || !device.UDPCSum ||
		tunnelEndpoint(device.SrcAddr) != tunnel.Local || tunnelEndpoint(device.Group) != tunnel.Remote {
		t.Fatalf("VXLAN device differs from configured fields: device=%+v tunnel=%+v settings=%+v", device, tunnel, settings)
	}
	expectedMTU := underlay.Attrs().MTU - interfaceintent.TunnelVXLANOverhead
	if connection.Link.MTU != nil {
		expectedMTU = int(*connection.Link.MTU)
	}
	if device.MTU != expectedMTU {
		t.Fatalf("VXLAN MTU=%d expected=%d", device.MTU, expectedMTU)
	}
}
