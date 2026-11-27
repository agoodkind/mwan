//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"net"
	"os"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

type TunnelRuntimeTopology struct {
	Gateway   netns.NsHandle
	Client    netns.NsHandle
	Isp       netns.NsHandle
	Alternate netns.NsHandle
	Endpoint  netns.NsHandle
	Remote    netns.NsHandle
}

func AddTunnelRuntimeAlternate(t *testing.T, topology *TunnelRuntimeTopology) {
	t.Helper()
	alternate := NewRuntimePeer(
		t,
		topology.Gateway,
		TunnelRuntimeAlternate,
		TunnelRuntimeAltLink,
		[]string{TunnelRuntimeAltLocal + "/30"},
		[]string{TunnelRuntimeAltGateway + "/30"},
		"",
	)
	topology.Alternate = alternate.Namespace
	LinkTunnelRuntimeNamespaces(
		t,
		topology.Alternate,
		"alt-far",
		[]string{TunnelRuntimeAltFar + "/30"},
		topology.Endpoint,
		"far-alt",
		[]string{TunnelRuntimeAltEndpoint + "/30"},
	)
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/far-alt/disable_ipv6", "1")
	AddMappedRuntimeRoute(t, "203.0.113.4/30", TunnelRuntimeAltFar, "far-alt")

	SetRuntimeNamespace(t, topology.Alternate)
	for _, name := range []string{TunnelRuntimeAltLink, "alt-far"} {
		WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", "1")
	}
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/ip_forward", "1")
	AddMappedRuntimeRoute(t, "198.51.100.0/30", TunnelRuntimeAltEndpoint, "alt-far")

	SetRuntimeNamespace(t, topology.Gateway)
	WriteTunnelRuntimeSetting(
		t,
		"/proc/sys/net/ipv6/conf/"+TunnelRuntimeAlternate+"/disable_ipv6",
		"1",
	)
	for _, scope := range []string{"all", TunnelRuntimeAlternate} {
		WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/conf/"+scope+"/rp_filter", "0")
	}
	link, err := netlink.LinkByName(TunnelRuntimeAlternate)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.ParseIP(TunnelRuntimeAltGateway),
		Priority:  TunnelRuntimeAltMetric,
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatalf("add the alternate default route: %v", err)
	}
}

func NewTunnelRuntimeNamespace(t *testing.T) netns.NsHandle {
	t.Helper()
	namespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	SetRuntimeLoopback(t)
	return namespace
}

func LinkTunnelRuntimeNamespaces(
	t *testing.T,
	left netns.NsHandle,
	leftName string,
	leftAddresses []string,
	right netns.NsHandle,
	rightName string,
	rightAddresses []string,
) {
	t.Helper()
	SetRuntimeNamespace(t, left)
	attributes := netlink.NewLinkAttrs()
	attributes.Name = leftName
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: attributes,
		PeerName:  rightName,
	}); err != nil {
		t.Fatal(err)
	}
	peer, err := netlink.LinkByName(rightName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(peer, int(right)); err != nil {
		t.Fatal(err)
	}
	ConfigureRuntimeLink(t, leftName, leftAddresses)
	SetRuntimeNamespace(t, right)
	ConfigureRuntimeLink(t, rightName, rightAddresses)
}

func WriteTunnelRuntimeSetting(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func BuildTunnelRuntimeTopology(t *testing.T, gateway netns.NsHandle) TunnelRuntimeTopology {
	t.Helper()
	management := NewRuntimePeer(
		t,
		gateway,
		"enmgmt0",
		"mgmt-host",
		[]string{"192.0.2.65/29"},
		[]string{"192.0.2.66/29"},
		"",
	)
	t.Cleanup(func() { _ = management.Namespace.Close() })
	clientAddresses := []string{TunnelRuntimeClientV6 + "/64"}
	for _, address := range TunnelRuntimeClientsV4() {
		clientAddresses = append(clientAddresses, address+"/29")
	}
	client := NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"},
		clientAddresses,
		"",
	)
	isp := NewRuntimePeer(
		t,
		gateway,
		TunnelRuntimeUnderlay,
		TunnelRuntimeISPLink,
		[]string{TunnelRuntimeLocal + "/30"},
		[]string{TunnelRuntimeISPGateway + "/30"},
		"",
	)
	topology := TunnelRuntimeTopology{
		Gateway: gateway,
		Client:  client.Namespace,
		Isp:     isp.Namespace,
	}
	topology.Endpoint = NewTunnelRuntimeNamespace(t)
	topology.Remote = NewTunnelRuntimeNamespace(t)
	AddTunnelRuntimeAlternate(t, &topology)
	for _, namespace := range []netns.NsHandle{
		topology.Client,
		topology.Isp,
		topology.Alternate,
		topology.Endpoint,
		topology.Remote,
	} {
		t.Cleanup(func() { _ = namespace.Close() })
	}
	LinkTunnelRuntimeNamespaces(
		t,
		topology.Isp,
		"isp-far",
		[]string{"198.51.100.1/30"},
		topology.Endpoint,
		"far-isp",
		[]string{TunnelRuntimeRemote + "/30"},
	)
	LinkTunnelRuntimeNamespaces(
		t,
		topology.Endpoint,
		"far-lan",
		[]string{"2001:db8:99::1/64"},
		topology.Remote,
		"remote-host",
		[]string{TunnelRuntimeRemoteClient + "/64"},
	)

	SetRuntimeNamespace(t, topology.Remote)
	AddRuntimeDefault(t, "remote-host", "2001:db8:99::1")

	SetRuntimeNamespace(t, topology.Endpoint)
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/far-isp/disable_ipv6", "1")
	AddRuntimeDefault(t, "far-isp", "198.51.100.1")
	attributes := netlink.NewLinkAttrs()
	attributes.Name = "far6in4"
	if err := netlink.LinkAdd(&netlink.Sittun{
		LinkAttrs: attributes,
		Ttl:       64,
		PMtuDisc:  1,
		Proto:     unix.IPPROTO_IPV6,
		Local:     net.ParseIP(TunnelRuntimeRemote),
		Remote:    net.ParseIP(TunnelRuntimeLocal),
	}); err != nil {
		t.Fatalf("create the remote sit device: %v", err)
	}
	ConfigureRuntimeLink(t, "far6in4", []string{TunnelRuntimeInnerRemote + "/64"})
	AddMappedRuntimeRoute(t, "2001:db8:b01:fe::/64", "", "far6in4")
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")

	SetRuntimeNamespace(t, topology.Isp)
	for _, name := range []string{TunnelRuntimeISPLink, "isp-far"} {
		WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", "1")
	}
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/ip_forward", "1")

	SetRuntimeNamespace(t, topology.Client)
	AddRuntimeDefault(t, "lan-host", "192.0.2.1")
	AddRuntimeDefault(t, "lan-host", "2001:db8:b01:fe::3")

	SetRuntimeNamespace(t, gateway)
	WriteTunnelRuntimeSetting(
		t,
		"/proc/sys/net/ipv6/conf/"+TunnelRuntimeUnderlay+"/disable_ipv6",
		"1",
	)
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/ip_forward", "1")
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
	AddRuntimeDefault(t, TunnelRuntimeUnderlay, TunnelRuntimeISPGateway)
	return topology
}
