//go:build linux && (firewall || protocol || systemd)

package testenv

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
	CombinedOrdinary         = "enord0"
	CombinedOrdinaryLink     = "ord-gw"
	CombinedOrdinaryLocalV4  = "203.0.113.10"
	CombinedOrdinaryPeerV4   = "203.0.113.9"
	CombinedOrdinaryLocalV6  = "2001:db8:c::2"
	CombinedOrdinaryPeerV6   = "2001:db8:c::1"
	CombinedOrdinaryMetric   = 200
	CombinedDestination      = "2001:db8:dead::1"
	CombinedPort             = 18513
	CombinedConnectRetry     = 2
	CombinedCheckInterval    = 1
	CombinedFailureThreshold = 1
	CombinedSessionLeaf      = `"keepalive":`
	CombinedInternalEntry    = `{"name":"enmwanbr0"`
)

type CombinedTopology struct {
	Tunnel       TunnelRuntimeTopology
	Destinations map[string]netns.NsHandle
}

func CombinedDirectProvider() BgpRuntimeProvider {
	provider := BgpRuntimeDirectProvider(BgpRuntimeLocalASN, BgpRuntimeRemoteASN, "::/0")
	provider.Table, provider.Mark, provider.Metric = 600, 4, 310
	return provider
}

func CombinedOrdinaryEntry() string {
	return fmt.Sprintf(
		`{"name":%q,"type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",`+
			`"ietf-ip:ipv4":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44"}},`+
			`"ietf-ip:ipv6":{"goodkind-mwan-steering:translation":{"mode":"native"}},`+
			`"goodkind-mwan-steering:wan":{"name":"ord","table-id":700,"fw-mark":5,"fw-mark-prio":700,"from-prio":65,`+
			`"health":{"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":%d,"recovery-threshold":1,`+
			`"check-interval":%d,"targets-v4":[%q],"targets-v6":[%q]}},`+
			`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}},`,
		CombinedOrdinary,
		CombinedFailureThreshold,
		CombinedCheckInterval,
		CombinedOrdinaryPeerV4,
		CombinedOrdinaryPeerV6,
	)
}

func WriteCombinedNetwork(t *testing.T, directory string, provider BgpRuntimeProvider) string {
	t.Helper()
	network := BgpTunnelRuntimeNetwork(t)
	if count := strings.Count(network, CombinedInternalEntry); count != 1 {
		t.Fatalf("internal interface entry occurs %d times, want 1", count)
	}
	network = strings.Replace(
		network,
		CombinedInternalEntry,
		provider.Entry()+","+CombinedOrdinaryEntry()+CombinedInternalEntry,
		1,
	)
	if count := strings.Count(network, CombinedSessionLeaf); count != 2 {
		t.Fatalf("session timer leaf occurs %d times, want 2", count)
	}
	network = strings.ReplaceAll(
		network,
		CombinedSessionLeaf,
		fmt.Sprintf(`"connect-retry":%d,`, CombinedConnectRetry)+CombinedSessionLeaf,
	)
	if err := os.WriteFile(
		filepath.Join(directory, "network.json"),
		[]byte(network),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	return network
}

func AddCombinedAddress(t *testing.T, device, cidr string) {
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

func BuildCombinedTopology(
	t *testing.T,
	run *BgpRuntimeRun,
	provider BgpRuntimeProvider,
) CombinedTopology {
	t.Helper()
	tunnel := BuildTunnelRuntimeTopology(t, run.Gateway)
	RemoveTunnelRuntimeReturnRoute(t, tunnel)
	router, remote := AddBGPRuntimeProvider(t, run.Gateway, provider)
	run.Topology = BgpRuntimeTopology{
		Gateway: run.Gateway,
		Client:  tunnel.Client,
		Isp:     tunnel.Isp,
		Routers: map[string]netns.NsHandle{
			provider.Id: router,
		},
		Remotes: map[string]netns.NsHandle{provider.Id: remote},
	}
	ordinary := NewRuntimePeer(
		t,
		run.Gateway,
		CombinedOrdinary,
		CombinedOrdinaryLink,
		[]string{CombinedOrdinaryLocalV4 + "/30", CombinedOrdinaryLocalV6 + "/64"},
		[]string{CombinedOrdinaryPeerV4 + "/30", CombinedOrdinaryPeerV6 + "/64"},
		"",
	)
	for _, namespace := range []netns.NsHandle{router, remote, ordinary.Namespace} {
		t.Cleanup(func() { _ = namespace.Close() })
	}
	destination := CombinedDestination + "/128"

	SetRuntimeNamespace(t, ordinary.Namespace)
	AddCombinedAddress(t, CombinedOrdinaryLink, destination)
	AddMappedRuntimeRoute(t, BgpRuntimeHome, CombinedOrdinaryLocalV6, CombinedOrdinaryLink)
	SetRuntimeNamespace(t, remote)
	AddCombinedAddress(t, "remote-host", destination)
	SetRuntimeNamespace(t, router)
	AddMappedRuntimeRoute(t, destination, provider.RemoteClient(), provider.RouterDevice+"-far")
	SetRuntimeNamespace(t, tunnel.Remote)
	AddCombinedAddress(t, "remote-host", destination)
	SetRuntimeNamespace(t, tunnel.Endpoint)
	AddMappedRuntimeRoute(t, destination, TunnelRuntimeRemoteClient, "far-lan")

	SetRuntimeNamespace(t, run.Gateway)
	AddCombinedOrdinaryDefaults(t)
	return CombinedTopology{
		Tunnel: tunnel,
		Destinations: map[string]netns.NsHandle{
			CombinedOrdinary:    ordinary.Namespace,
			provider.Device:     remote,
			TunnelRuntimeDevice: tunnel.Remote,
		},
	}
}

func AddCombinedOrdinaryDefaults(t *testing.T) {
	t.Helper()
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv4/conf/"+CombinedOrdinary+"/rp_filter", "0")
	link, err := netlink.LinkByName(CombinedOrdinary)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.ParseIP(CombinedOrdinaryPeerV4),
		Priority:  CombinedOrdinaryMetric,
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatalf("add the ordinary IPv4 default route: %v", err)
	}
	AddRuntimeDefault(t, CombinedOrdinary, CombinedOrdinaryPeerV6)
}
