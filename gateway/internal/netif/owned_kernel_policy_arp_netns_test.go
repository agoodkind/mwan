//go:build linux && netns

package netif

import (
	"bytes"
	"errors"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	"github.com/mdlayher/packet"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

func TestOwnedKernelPolicyARP(t *testing.T) {
	const childEnv = "MWAN_OWNED_KERNEL_ARP_CHILD"
	if os.Getenv(childEnv) != "1" {
		child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestOwnedKernelPolicyARP$", "-test.v")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		output, err := child.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated ARP ownership test: %v: %s", err, output)
		}
		t.Log(string(output))
		return
	}

	const transitIP = "198.51.100.1"
	provider, providerPeer := kernelARPLink(t, "arp-provider", "provider-peer", "192.0.2.1/24")
	transit, transitPeer := kernelARPLink(t, "arp-transit", "transit-peer", transitIP+"/24")
	external, _ := kernelARPLink(t, "arp-external", "external-peer", "203.0.113.1/24")
	connections := []interfaceintent.Connection{
		kernelARPConnection(provider, interfaceintent.OwnerMWAN, interfaceintent.RoleProvider),
		kernelARPConnection(transit, interfaceintent.OwnerMWAN, interfaceintent.RoleInternal),
		kernelARPConnection(external, interfaceintent.OwnerExternal, interfaceintent.RoleManagement),
	}
	results := make([]OwnedLinkResult, 0, len(connections))
	for i, link := range []netlink.Link{provider, transit, external} {
		results = append(results, OwnedLinkResult{
			ConnectionID: connections[i].ID.String(), Name: link.Attrs().Name,
			ActualName: link.Attrs().Name, IfIndex: link.Attrs().Index, Status: OwnedLinkReady,
		})
	}
	sysctl := NewProcSysctlRunner(slog.Default(), false)
	for _, iface := range []string{"all", "default", provider.Attrs().Name, transit.Attrs().Name, external.Attrs().Name} {
		assertKernelARPSetting(t, sysctl, iface, "0")
	}
	assertKernelARPReply(t, providerPeer, transitIP, provider.Attrs().HardwareAddr)
	assertKernelARPReply(t, transitPeer, transitIP, transit.Attrs().HardwareAddr)

	journal := filepath.Join(t.TempDir(), "kernel-policy.json")
	reconciler, err := NewOwnedKernelPolicyReconciler(journal, sysctl)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciler.Reconcile(t.Context(), connections, results); err != nil {
		t.Fatal(err)
	}
	assertKernelARPSetting(t, sysctl, provider.Attrs().Name, "1")
	assertKernelARPSetting(t, sysctl, transit.Attrs().Name, "1")
	for _, iface := range []string{"all", "default", external.Attrs().Name} {
		assertKernelARPSetting(t, sysctl, iface, "0")
	}
	assertKernelARPReply(t, providerPeer, transitIP, nil)
	assertKernelARPReply(t, transitPeer, transitIP, transit.Attrs().HardwareAddr)

	restarted, err := NewOwnedKernelPolicyReconciler(journal, sysctl)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.Reconcile(t.Context(), connections, results); err != nil {
		t.Fatal(err)
	}
	assertKernelARPReply(t, providerPeer, transitIP, nil)
	connections[0].Owner = interfaceintent.OwnerExternal
	connections[1].Owner = interfaceintent.OwnerNetworkd
	if err := restarted.Reconcile(t.Context(), connections, results); err != nil {
		t.Fatal(err)
	}
	assertKernelARPSetting(t, sysctl, provider.Attrs().Name, "0")
	assertKernelARPSetting(t, sysctl, transit.Attrs().Name, "0")
	assertKernelARPReply(t, providerPeer, transitIP, provider.Attrs().HardwareAddr)
	assertKernelARPReply(t, transitPeer, transitIP, transit.Attrs().HardwareAddr)
}

func kernelARPLink(t *testing.T, name, peerName, address string) (netlink.Link, *net.Interface) {
	t.Helper()
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: peerName}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := netlink.ParseAddr(address)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(link, parsed); err != nil {
		t.Fatal(err)
	}
	peer, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatal(err)
	}
	for _, device := range []netlink.Link{link, peer} {
		if err := netlink.LinkSetUp(device); err != nil {
			t.Fatal(err)
		}
	}
	device, err := net.InterfaceByName(peerName)
	if err != nil {
		t.Fatal(err)
	}
	return link, device
}

func kernelARPConnection(link netlink.Link, owner interfaceintent.Owner, role interfaceintent.Role) interfaceintent.Connection {
	return interfaceintent.Connection{
		ID: connectionid.ID(link.Attrs().Name), Name: link.Attrs().Name, Owner: owner, Roles: role,
		Link: &interfaceintent.Link{Kind: interfaceintent.KindPhysical, Match: interfaceintent.Match{HardwareAddress: link.Attrs().HardwareAddr.String()}},
	}
}

func assertKernelARPSetting(t *testing.T, sysctl SysctlRunner, iface, expected string) {
	t.Helper()
	value, err := sysctl.Get(t.Context(), "net.ipv4.conf."+iface+".arp_ignore")
	if err != nil || value != expected {
		t.Fatalf("%s arp_ignore = %q, want %s: %v", iface, value, expected, err)
	}
}

func assertKernelARPReply(t *testing.T, peer *net.Interface, target string, expected net.HardwareAddr) {
	t.Helper()
	connection, err := packet.Listen(peer, packet.Raw, unix.ETH_P_ARP, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	const replyTimeout = 300 * time.Millisecond
	if err := connection.SetDeadline(time.Now().Add(replyTimeout)); err != nil {
		t.Fatal(err)
	}
	broadcast := net.HardwareAddr{255, 255, 255, 255, 255, 255}
	targetIP := net.ParseIP(target).To4()
	frame := gopacket.NewSerializeBuffer()
	if err := gopacket.SerializeLayers(frame, gopacket.SerializeOptions{FixLengths: true},
		&layers.Ethernet{SrcMAC: peer.HardwareAddr, DstMAC: broadcast, EthernetType: layers.EthernetTypeARP},
		&layers.ARP{
			AddrType: layers.LinkTypeEthernet, Protocol: layers.EthernetTypeIPv4,
			HwAddressSize: 6, ProtAddressSize: 4, Operation: layers.ARPRequest,
			SourceHwAddress: peer.HardwareAddr, SourceProtAddress: net.IPv4(198, 51, 100, 2).To4(),
			DstHwAddress: make([]byte, 6), DstProtAddress: targetIP,
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.WriteTo(frame.Bytes(), &packet.Addr{HardwareAddr: broadcast}); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1500)
	for {
		count, _, err := connection.ReadFrom(buffer)
		if err != nil {
			var timeout net.Error
			if expected == nil && errors.As(err, &timeout) && timeout.Timeout() {
				return
			}
			t.Fatalf("ARP reply for %s on %s: %v", target, peer.Name, err)
		}
		decoded := gopacket.NewPacket(buffer[:count], layers.LayerTypeEthernet, gopacket.Default)
		reply, ok := decoded.Layer(layers.LayerTypeARP).(*layers.ARP)
		if !ok || reply.Operation != layers.ARPReply || !bytes.Equal(reply.SourceProtAddress, targetIP) {
			continue
		}
		if expected == nil || !bytes.Equal(reply.SourceHwAddress, expected) {
			t.Fatalf("ARP reply for %s on %s used %s, want %s", target, peer.Name, net.HardwareAddr(reply.SourceHwAddress), expected)
		}
		return
	}
}
