//go:build linux && netns

package firewall

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/ifmgr"
)

type packetPeer struct {
	namespace netns.NsHandle
	gateway   string
}

func TestFirewallReconcileAcceptsPackets(t *testing.T) {
	for _, layout := range []struct {
		name       string
		management string
		lan        string
		wan        string
	}{
		{name: "gateway links", management: "mgmt0", lan: "lan0", wan: "wan0"},
		{name: "renamed logical links", management: "veth-mgmt", lan: "br-lan", wan: "tun-wan"},
	} {
		t.Run(layout.name, func(t *testing.T) {
			testFirewallPackets(t, layout.management, layout.lan, layout.wan)
		})
	}
}

func testFirewallPackets(t *testing.T, managementName, lanName, wanName string) {
	runtime.LockOSThread()
	previous, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	gateway, err := netns.New()
	if err != nil {
		previous.Close()
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netns.Set(previous); err != nil {
			t.Error(err)
		}
		gateway.Close()
		previous.Close()
		runtime.UnlockOSThread()
	})
	setPacketLoopback(t)
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/all/forwarding", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	management := newPacketPeer(t, gateway, managementName, "mgmt-host", []string{"203.0.113.1/24"}, []string{"203.0.113.2/24", "203.0.113.3/24"})
	lan := newPacketPeer(t, gateway, lanName, "lan-host", []string{"192.0.2.1/24", "2001:db8:1::1/64"}, []string{"192.0.2.2/24", "192.0.2.3/24", "2001:db8:1::2/64"})
	wan := newPacketPeer(t, gateway, wanName, "wan-host", []string{"198.51.100.1/24", "198.51.100.10/32", "2001:db8:2::1/64"}, []string{"198.51.100.2/24", "198.51.100.3/24", "2001:db8:2::2/64"})
	addPacketRoute(t, lan.namespace, gateway, "198.51.100.0/24", "192.0.2.1")
	addPacketRoute(t, lan.namespace, gateway, "2001:db8:2::/64", "2001:db8:1::1")
	addPacketRoute(t, wan.namespace, gateway, "192.0.2.0/24", "198.51.100.1")
	addPacketRoute(t, wan.namespace, gateway, "2001:db8:1::/64", "2001:db8:2::1")

	policy := firewall.Config{
		Enabled:             true,
		InternalInterface:   lan.gateway,
		InternalNetworkIPv4: netip.MustParsePrefix("192.0.2.0/24"),
		ManagementInterface: management.gateway,
		ManagementServices:  []firewall.Service{{Protocol: "tcp", Port: 2222, Sources: []netip.Prefix{netip.MustParsePrefix("203.0.113.2/32")}}},
		KnownInterfaces:     []string{management.gateway, lan.gateway, wan.gateway},
		LocalPermits: []firewall.TransportPermit{
			{InputInterface: wan.gateway, Family: firewall.IPv4, Source: netip.MustParsePrefix("198.51.100.2/32"), Destination: netip.MustParsePrefix("198.51.100.1/32"), Protocol: "tcp", DestinationPort: 179},
			{InputInterface: wan.gateway, Family: firewall.IPv4, Source: netip.MustParsePrefix("198.51.100.2/32"), Destination: netip.MustParsePrefix("198.51.100.1/32"), Protocol: "gre"},
		},
		Paths:     []firewall.ForwardingPath{{InternalInterface: lan.gateway, ExternalInterface: wan.gateway, IPv4: true, IPv6: true}},
		Providers: []firewall.Provider{{Interface: wan.gateway, Mark: 101, MasqueradeIPv4: true, StaticMappings: []firewall.Mapping{{External: netip.MustParseAddr("198.51.100.10"), Internal: netip.MustParseAddr("192.0.2.2")}}}},
	}
	selected, err := New(policy)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := selected.Init(ctx, &ifmgr.Env{Log: logger}); err != nil {
		t.Fatal(err)
	}
	if err := selected.Reconcile(ctx, logger); err != nil {
		t.Fatal(err)
	}
	observePacketMark(t, wan.gateway)

	assertPacketTCP(t, gateway, management.namespace, "tcp4", "203.0.113.1:2222", "203.0.113.2:0", true, "203.0.113.2")
	assertPacketTCP(t, gateway, management.namespace, "tcp4", "203.0.113.1:2222", "203.0.113.3:0", false, "")
	assertPacketTCP(t, gateway, lan.namespace, "tcp4", "192.0.2.1:2222", "192.0.2.2:0", false, "")
	assertPacketTCP(t, gateway, wan.namespace, "tcp4", "198.51.100.1:179", "198.51.100.2:0", true, "198.51.100.2")
	assertPacketTCP(t, gateway, wan.namespace, "tcp4", "198.51.100.1:179", "198.51.100.3:0", false, "")
	assertPacketTCP(t, gateway, lan.namespace, "tcp4", "192.0.2.1:179", "192.0.2.2:0", false, "")
	assertPacketTCP(t, wan.namespace, lan.namespace, "tcp4", "198.51.100.2:3001", "192.0.2.3:0", true, "198.51.100.1")
	assertPacketTCP(t, lan.namespace, wan.namespace, "tcp4", "192.0.2.2:3002", "198.51.100.2:0", true, "198.51.100.2")
	assertPacketTCP(t, wan.namespace, lan.namespace, "tcp4", "198.51.100.2:3003", "192.0.2.2:0", true, "198.51.100.10")
	assertPacketTCPTo(t, lan.namespace, wan.namespace, "tcp4", "192.0.2.2:3005", "198.51.100.10:3005", "198.51.100.2:0", true, "198.51.100.2")
	setPacketNamespace(t, gateway)
	assertObservedPacketMark(t)
	assertPacketTCP(t, wan.namespace, lan.namespace, "tcp6", "[2001:db8:2::2]:3004", "[2001:db8:1::2]:0", true, "2001:db8:1::2")

	assertPacketGRE(t, gateway, wan.namespace, "198.51.100.2", true)
	assertPacketGRE(t, gateway, wan.namespace, "198.51.100.3", false)
	assertPacketGRE(t, gateway, lan.namespace, "192.0.2.2", false)
}

func newPacketPeer(t *testing.T, gateway netns.NsHandle, gatewayName, peerName string, gatewayAddresses, peerAddresses []string) packetPeer {
	t.Helper()
	peerNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	setPacketLoopback(t)
	setPacketNamespace(t, gateway)
	linkName := gatewayName
	if gatewayName == "br-lan" {
		linkName = "lan-port"
	}
	attributes := netlink.NewLinkAttrs()
	attributes.Name = linkName
	peer := &netlink.Veth{LinkAttrs: attributes, PeerName: peerName}
	if err := netlink.LinkAdd(peer); err != nil {
		t.Fatal(err)
	}
	peerLink, err := netlink.LinkByName(peerName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(peerLink, int(peerNamespace)); err != nil {
		t.Fatal(err)
	}
	if gatewayName == "br-lan" {
		bridgeAttributes := netlink.NewLinkAttrs()
		bridgeAttributes.Name = gatewayName
		bridge := &netlink.Bridge{LinkAttrs: bridgeAttributes}
		if err := netlink.LinkAdd(bridge); err != nil {
			t.Fatal(err)
		}
		gatewayLink, err := netlink.LinkByName(linkName)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetMaster(gatewayLink, bridge); err != nil {
			t.Fatal(err)
		}
		configurePacketLink(t, linkName, nil)
	}
	configurePacketLink(t, gatewayName, gatewayAddresses)
	setPacketNamespace(t, peerNamespace)
	configurePacketLink(t, peerName, peerAddresses)
	setPacketNamespace(t, gateway)
	t.Cleanup(func() { peerNamespace.Close() })
	return packetPeer{namespace: peerNamespace, gateway: gatewayName}
}

func setPacketNamespace(t *testing.T, namespace netns.NsHandle) {
	t.Helper()
	if err := netns.Set(namespace); err != nil {
		t.Fatal(err)
	}
}

func setPacketLoopback(t *testing.T) {
	t.Helper()
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatal(err)
	}
}

func configurePacketLink(t *testing.T, name string, addresses []string) {
	t.Helper()
	link, err := netlink.LinkByName(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		parsed, err := netlink.ParseAddr(address)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.IP.To4() == nil {
			parsed.Flags = unix.IFA_F_NODAD
		}
		if err := netlink.AddrAdd(link, parsed); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
}

func addPacketRoute(t *testing.T, namespace, gateway netns.NsHandle, destination, nextHop string) {
	t.Helper()
	setPacketNamespace(t, namespace)
	prefix := netip.MustParsePrefix(destination)
	_, network, err := net.ParseCIDR(prefix.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{Dst: network, Gw: net.ParseIP(nextHop)}); err != nil {
		t.Fatal(err)
	}
	setPacketNamespace(t, gateway)
}

func assertPacketTCP(t *testing.T, destinationNamespace, sourceNamespace netns.NsHandle, network, destination, source string, accepted bool, expectedSource string) {
	t.Helper()
	assertPacketTCPTo(t, destinationNamespace, sourceNamespace, network, destination, destination, source, accepted, expectedSource)
}

func assertPacketTCPTo(t *testing.T, destinationNamespace, sourceNamespace netns.NsHandle, network, listenAddress, dialAddress, source string, accepted bool, expectedSource string) {
	t.Helper()
	setPacketNamespace(t, destinationNamespace)
	listener, err := net.Listen(network, listenAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	acceptedAddress := make(chan string, 1)
	go func() {
		connection, acceptError := listener.Accept()
		if acceptError == nil {
			acceptedAddress <- connection.RemoteAddr().(*net.TCPAddr).IP.String()
			connection.Close()
		}
	}()
	setPacketNamespace(t, sourceNamespace)
	local, err := net.ResolveTCPAddr(network, source)
	if err != nil {
		t.Fatal(err)
	}
	dialer := net.Dialer{LocalAddr: local, Timeout: 500 * time.Millisecond}
	connection, dialError := dialer.Dial(network, dialAddress)
	if connection != nil {
		connection.Close()
	}
	if accepted && dialError != nil {
		t.Fatalf("%s from %s rejected: %v", dialAddress, source, dialError)
	}
	if !accepted && dialError == nil {
		t.Fatalf("%s from %s was accepted", dialAddress, source)
	}
	setPacketNamespace(t, destinationNamespace)
	if accepted {
		select {
		case observed := <-acceptedAddress:
			if observed != expectedSource {
				t.Fatalf("%s from %s arrived with source %s, want %s", dialAddress, source, observed, expectedSource)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s from %s was connected but not accepted", dialAddress, source)
		}
	}
}

func assertPacketGRE(t *testing.T, gateway, sourceNamespace netns.NsHandle, source string, accepted bool) {
	t.Helper()
	setPacketNamespace(t, gateway)
	listener, err := net.ListenPacket("ip4:47", "198.51.100.1")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := listener.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	setPacketNamespace(t, sourceNamespace)
	local := &net.IPAddr{IP: net.ParseIP(source)}
	remote := &net.IPAddr{IP: net.ParseIP("198.51.100.1")}
	connection, err := net.DialIP("ip4:47", local, remote)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("packet-" + strconv.FormatInt(time.Now().UnixNano(), 10))); err != nil {
		t.Fatal(err)
	}
	connection.Close()
	setPacketNamespace(t, gateway)
	buffer := make([]byte, 128)
	_, address, readError := listener.ReadFrom(buffer)
	if accepted && readError != nil {
		t.Fatalf("GRE from %s rejected: %v", source, readError)
	}
	if !accepted && readError == nil {
		t.Fatalf("GRE from %s was accepted from %s", source, address)
	}
	if accepted && !strings.HasPrefix(address.String(), source) {
		t.Fatalf("GRE source = %s, want %s", address, source)
	}
}

func observePacketMark(t *testing.T, providerInterface string) {
	t.Helper()
	runPacketNFT(t, "add", "table", "inet", "packet_observe")
	runPacketNFT(t, "add", "chain", "inet", "packet_observe", "prerouting", "{ type filter hook prerouting priority -149; policy accept; }")
	runPacketNFT(t, "add", "rule", "inet", "packet_observe", "prerouting", "iifname", providerInterface, "meta", "mark", "101", "counter")
}

func assertObservedPacketMark(t *testing.T) {
	t.Helper()
	output := runPacketNFT(t, "list", "chain", "inet", "packet_observe", "prerouting")
	match := regexp.MustCompile(`counter packets ([0-9]+)`).FindStringSubmatch(output)
	if len(match) != 2 || match[1] == "0" {
		t.Fatalf("inbound packet did not receive the provider mark: %s", output)
	}
}

func runPacketNFT(t *testing.T, arguments ...string) string {
	t.Helper()
	output, err := exec.Command("nft", arguments...).CombinedOutput()
	if err != nil {
		t.Fatalf("nft %v: %v: %s", arguments, err, output)
	}
	return string(output)
}
