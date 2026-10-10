//go:build linux && firewallnetns

package main

import (
	"net"
	"os"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

type tunnelRuntimeTopology struct {
	gateway   netns.NsHandle
	client    netns.NsHandle
	isp       netns.NsHandle
	alternate netns.NsHandle
	endpoint  netns.NsHandle
	remote    netns.NsHandle
}

// The gateway accepts replies on the alternate link because the reverse path filter is off on that link.
func addTunnelRuntimeAlternate(t *testing.T, topology *tunnelRuntimeTopology) {
	t.Helper()
	alternate := newRuntimePeer(t, topology.gateway, tunnelRuntimeAlternate, tunnelRuntimeAltLink,
		[]string{tunnelRuntimeAltLocal + "/30"}, []string{tunnelRuntimeAltGateway + "/30"}, "")
	topology.alternate = alternate.namespace
	linkTunnelRuntimeNamespaces(t, topology.alternate, "alt-far", []string{tunnelRuntimeAltFar + "/30"},
		topology.endpoint, "far-alt", []string{tunnelRuntimeAltEndpoint + "/30"})
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/far-alt/disable_ipv6", "1")
	addMappedRuntimeRoute(t, "203.0.113.4/30", tunnelRuntimeAltFar, "far-alt")

	setRuntimeNamespace(t, topology.alternate)
	for _, name := range []string{tunnelRuntimeAltLink, "alt-far"} {
		writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", "1")
	}
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/ip_forward", "1")
	addMappedRuntimeRoute(t, "198.51.100.0/30", tunnelRuntimeAltEndpoint, "alt-far")

	setRuntimeNamespace(t, topology.gateway)
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/"+tunnelRuntimeAlternate+"/disable_ipv6", "1")
	for _, scope := range []string{"all", tunnelRuntimeAlternate} {
		writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/conf/"+scope+"/rp_filter", "0")
	}
	link, err := netlink.LinkByName(tunnelRuntimeAlternate)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(tunnelRuntimeAltGateway), Priority: tunnelRuntimeAltMetric}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatalf("add the alternate default route: %v", err)
	}
}

func newTunnelRuntimeNamespace(t *testing.T) netns.NsHandle {
	t.Helper()
	namespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	setRuntimeLoopback(t)
	return namespace
}

func linkTunnelRuntimeNamespaces(t *testing.T, left netns.NsHandle, leftName string, leftAddresses []string, right netns.NsHandle, rightName string, rightAddresses []string) {
	t.Helper()
	setRuntimeNamespace(t, left)
	attributes := netlink.NewLinkAttrs()
	attributes.Name = leftName
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: attributes, PeerName: rightName}); err != nil {
		t.Fatal(err)
	}
	peer, err := netlink.LinkByName(rightName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(peer, int(right)); err != nil {
		t.Fatal(err)
	}
	configureRuntimeLink(t, leftName, leftAddresses)
	setRuntimeNamespace(t, right)
	configureRuntimeLink(t, rightName, rightAddresses)
}

func writeTunnelRuntimeSetting(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// The ISP link has IPv6 disabled at both ends because the ISP provides IPv4 only.
func buildTunnelRuntimeTopology(t *testing.T, gateway netns.NsHandle) tunnelRuntimeTopology {
	t.Helper()
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"192.0.2.65/29"}, []string{"192.0.2.66/29"}, "")
	t.Cleanup(func() { _ = management.namespace.Close() })
	clientAddresses := []string{tunnelRuntimeClientV6 + "/64"}
	for _, address := range tunnelRuntimeClientsV4() {
		clientAddresses = append(clientAddresses, address+"/29")
	}
	client := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"}, clientAddresses, "")
	isp := newRuntimePeer(t, gateway, tunnelRuntimeUnderlay, tunnelRuntimeISPLink,
		[]string{tunnelRuntimeLocal + "/30"}, []string{tunnelRuntimeISPGateway + "/30"}, "")
	topology := tunnelRuntimeTopology{gateway: gateway, client: client.namespace, isp: isp.namespace}
	topology.endpoint = newTunnelRuntimeNamespace(t)
	topology.remote = newTunnelRuntimeNamespace(t)
	addTunnelRuntimeAlternate(t, &topology)
	for _, namespace := range []netns.NsHandle{topology.client, topology.isp, topology.alternate, topology.endpoint, topology.remote} {
		t.Cleanup(func() { _ = namespace.Close() })
	}
	linkTunnelRuntimeNamespaces(t, topology.isp, "isp-far", []string{"198.51.100.1/30"}, topology.endpoint, "far-isp", []string{tunnelRuntimeRemote + "/30"})
	linkTunnelRuntimeNamespaces(t, topology.endpoint, "far-lan", []string{"2001:db8:99::1/64"}, topology.remote, "remote-host", []string{tunnelRuntimeRemoteClient + "/64"})

	setRuntimeNamespace(t, topology.remote)
	addRuntimeDefault(t, "remote-host", "2001:db8:99::1")

	setRuntimeNamespace(t, topology.endpoint)
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/far-isp/disable_ipv6", "1")
	addRuntimeDefault(t, "far-isp", "198.51.100.1")
	attributes := netlink.NewLinkAttrs()
	attributes.Name = "far6in4"
	if err := netlink.LinkAdd(&netlink.Sittun{
		LinkAttrs: attributes, Ttl: 64, PMtuDisc: 1, Proto: unix.IPPROTO_IPV6,
		Local: net.ParseIP(tunnelRuntimeRemote), Remote: net.ParseIP(tunnelRuntimeLocal),
	}); err != nil {
		t.Fatalf("create the remote sit device: %v", err)
	}
	configureRuntimeLink(t, "far6in4", []string{tunnelRuntimeInnerRemote + "/64"})
	addMappedRuntimeRoute(t, "2001:db8:b01:fe::/64", "", "far6in4")
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")

	setRuntimeNamespace(t, topology.isp)
	for _, name := range []string{tunnelRuntimeISPLink, "isp-far"} {
		writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", "1")
	}
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/ip_forward", "1")

	setRuntimeNamespace(t, topology.client)
	addRuntimeDefault(t, "lan-host", "192.0.2.1")
	addRuntimeDefault(t, "lan-host", "2001:db8:b01:fe::3")

	setRuntimeNamespace(t, gateway)
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/"+tunnelRuntimeUnderlay+"/disable_ipv6", "1")
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/ip_forward", "1")
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
	addRuntimeDefault(t, tunnelRuntimeUnderlay, tunnelRuntimeISPGateway)
	return topology
}
