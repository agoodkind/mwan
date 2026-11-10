//go:build linux && firewallnetns

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	combinedOrdinary         = "enord0"
	combinedOrdinaryLink     = "ord-gw"
	combinedOrdinaryLocalV4  = "203.0.113.10"
	combinedOrdinaryPeerV4   = "203.0.113.9"
	combinedOrdinaryLocalV6  = "2001:db8:c::2"
	combinedOrdinaryPeerV6   = "2001:db8:c::1"
	combinedOrdinaryMetric   = 200
	combinedDestination      = "2001:db8:dead::1"
	combinedPort             = 18513
	combinedConnectRetry     = 2
	combinedCheckInterval    = 1
	combinedFailureThreshold = 2
	combinedSessionLeaf      = `"keepalive":`
	combinedInternalEntry    = `{"name":"enmwanbr0"`
)

type combinedTopology struct {
	tunnel       tunnelRuntimeTopology
	destinations map[string]netns.NsHandle
}

func combinedDirectProvider() bgpRuntimeProvider {
	provider := bgpRuntimeDirectProvider(bgpRuntimeLocalASN, bgpRuntimeRemoteASN, "::/0")
	provider.table, provider.mark, provider.metric = 600, 4, 310
	return provider
}

func combinedOrdinaryEntry() string {
	return fmt.Sprintf(`{"name":%q,"type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",`+
		`"ietf-ip:ipv4":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44"}},`+
		`"ietf-ip:ipv6":{"goodkind-mwan-steering:translation":{"mode":"native"}},`+
		`"goodkind-mwan-steering:wan":{"name":"ord","table-id":700,"fw-mark":5,"fw-mark-prio":700,"from-prio":65,`+
		`"health":{"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":%d,"recovery-threshold":1,`+
		`"check-interval":%d,"targets-v4":[%q],"targets-v6":[%q]}},`+
		`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}},`,
		combinedOrdinary, combinedFailureThreshold, combinedCheckInterval, combinedOrdinaryPeerV4, combinedOrdinaryPeerV6)
}

func writeCombinedNetwork(t *testing.T, directory string, provider bgpRuntimeProvider) string {
	t.Helper()
	network := bgpTunnelRuntimeNetwork(t)
	if count := strings.Count(network, combinedInternalEntry); count != 1 {
		t.Fatalf("internal interface entry occurs %d times, want 1", count)
	}
	network = strings.Replace(network, combinedInternalEntry, provider.entry()+","+combinedOrdinaryEntry()+combinedInternalEntry, 1)
	if count := strings.Count(network, combinedSessionLeaf); count != 2 {
		t.Fatalf("session timer leaf occurs %d times, want 2", count)
	}
	network = strings.ReplaceAll(network, combinedSessionLeaf, fmt.Sprintf(`"connect-retry":%d,`, combinedConnectRetry)+combinedSessionLeaf)
	if err := os.WriteFile(filepath.Join(directory, "network.json"), []byte(network), 0o600); err != nil {
		t.Fatal(err)
	}
	return network
}

func addCombinedAddress(t *testing.T, device, cidr string) {
	t.Helper()
	link, err := netlink.LinkByName(device)
	if err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	address.Flags = unix.IFA_F_NODAD
	if err := netlink.AddrAdd(link, address); err != nil {
		t.Fatalf("add %s to %s: %v", cidr, device, err)
	}
}

func buildCombinedTopology(t *testing.T, run *bgpRuntimeRun, provider bgpRuntimeProvider) combinedTopology {
	t.Helper()
	tunnel := buildTunnelRuntimeTopology(t, run.gateway)
	removeTunnelRuntimeReturnRoute(t, tunnel)
	router, remote := addBGPRuntimeProvider(t, run.gateway, provider)
	run.topology = bgpRuntimeTopology{
		gateway: run.gateway, client: tunnel.client, isp: tunnel.isp,
		routers: map[string]netns.NsHandle{provider.id: router}, remotes: map[string]netns.NsHandle{provider.id: remote},
	}
	ordinary := newRuntimePeer(t, run.gateway, combinedOrdinary, combinedOrdinaryLink,
		[]string{combinedOrdinaryLocalV4 + "/30", combinedOrdinaryLocalV6 + "/64"},
		[]string{combinedOrdinaryPeerV4 + "/30", combinedOrdinaryPeerV6 + "/64"}, "")
	for _, namespace := range []netns.NsHandle{router, remote, ordinary.namespace} {
		t.Cleanup(func() { _ = namespace.Close() })
	}
	destination := combinedDestination + "/128"

	setRuntimeNamespace(t, ordinary.namespace)
	addCombinedAddress(t, combinedOrdinaryLink, destination)
	addMappedRuntimeRoute(t, bgpRuntimeHome, combinedOrdinaryLocalV6, combinedOrdinaryLink)
	setRuntimeNamespace(t, remote)
	addCombinedAddress(t, "remote-host", destination)
	setRuntimeNamespace(t, router)
	addMappedRuntimeRoute(t, destination, provider.remoteClient(), provider.routerDevice+"-far")
	setRuntimeNamespace(t, tunnel.remote)
	addCombinedAddress(t, "remote-host", destination)
	setRuntimeNamespace(t, tunnel.endpoint)
	addMappedRuntimeRoute(t, destination, tunnelRuntimeRemoteClient, "far-lan")

	setRuntimeNamespace(t, run.gateway)
	addCombinedOrdinaryDefaults(t)
	return combinedTopology{tunnel: tunnel, destinations: map[string]netns.NsHandle{
		combinedOrdinary: ordinary.namespace, provider.device: remote, tunnelRuntimeDevice: tunnel.remote,
	}}
}

func addCombinedOrdinaryDefaults(t *testing.T) {
	t.Helper()
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/conf/"+combinedOrdinary+"/rp_filter", "0")
	link, err := netlink.LinkByName(combinedOrdinary)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Gw: net.ParseIP(combinedOrdinaryPeerV4), Priority: combinedOrdinaryMetric}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatalf("add the ordinary IPv4 default route: %v", err)
	}
	addRuntimeDefault(t, combinedOrdinary, combinedOrdinaryPeerV6)
}
