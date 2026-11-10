//go:build linux && firewallnetns

package main

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netns"
)

const (
	bgpUpstreamNetwork      = "2001:db8:50::/64"
	bgpUpstreamAddress      = "2001:db8:50::1"
	bgpUpstreamVPSAddress   = "2001:db8:50::2"
	bgpUpstreamRemoteRouter = "2001:db8:51::1"
	bgpUpstreamRemoteClient = "2001:db8:51::2"
	bgpUpstreamTunnelPrefix = "2001:db8:6::/64"
	bgpUpstreamASN          = 64514
	bgpUpstreamVPSDevice    = "far-up"
	bgpUpstreamDevice       = "up-vps"
)

type bgpTunnelRuntimeSession struct {
	peer        string
	remoteASN   uint32
	multihopTTL int
	routed      string
	tier        int
}

func (s bgpTunnelRuntimeSession) entry() string {
	multihop, routes := "", ""
	if s.multihopTTL > 0 {
		multihop = fmt.Sprintf(`"multihop-ttl":%d,`, s.multihopTTL)
	}
	if s.routed != "" {
		routes = fmt.Sprintf(`"goodkind-mwan-steering:route":[{"destination":%q,"gateway":%q}],`, s.routed, tunnelRuntimeInnerRemote)
	}
	session := fmt.Sprintf(`"bgp-session":[{"name":"remote","peer-address":%q,"local-address":%q,%s`+
		`"local-as":%d,"remote-as":%d,"family":["ipv6-unicast"],"keepalive":%d,"hold":%d,"route-metric":300,`+
		`"import":[{"prefix":"::/0","min-length":0,"max-length":0}],`+
		`"export":[{"prefix":%q,"mode":"always","next-hop":%q}]}]`,
		s.peer, tunnelRuntimeInnerLocal, multihop, bgpRuntimeLocalASN, s.remoteASN,
		bgpRuntimeKeepalive, bgpRuntimeHold, bgpRuntimeHome, tunnelRuntimeInnerLocal)
	return fmt.Sprintf(`{"name":%q,"type":"iana-if-type:tunnel",`+
		`"goodkind-mwan-steering:connection-id":"tunnel-6in4","goodkind-mwan-steering:owner":"mwan",`+
		`"goodkind-mwan-steering:link":{"mtu":%d,"tunnel":{"protocol":"6in4","underlay":%q,"remote-address":%q,`+
		`"local-address":%q,"ttl":64}},`+
		`"ietf-ip:ipv6":{"address":[{"ip":%q,"prefix-length":64}],%s`+
		`"goodkind-mwan-steering:translation":{"mode":"native"}},`+
		`"goodkind-mwan-steering:wan":{"name":"tunnel","table-id":200,"fw-mark":2,"fw-mark-prio":200,"from-prio":56,`+
		`"health":{"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":2,"recovery-threshold":1,`+
		`"check-interval":1,"targets-v6":[%q]},%s},`+
		`"goodkind-mwan-steering:steering":{"tier":%d,"weight":1}},`,
		tunnelRuntimeDevice, tunnelRuntimeMTU, tunnelRuntimeUnderlay, tunnelRuntimeRemote, tunnelRuntimeLocal,
		tunnelRuntimeInnerLocal, routes, bgpUpstreamRemoteClient, session, s.tier)
}

func writeBGPTunnelRuntimeNetwork(t *testing.T, directory string, session bgpTunnelRuntimeSession, providers ...bgpRuntimeProvider) {
	t.Helper()
	internal := `{"name":"enmwanbr0","type":"iana-if-type:other"}`
	network := bgpRuntimeNetwork(providers...)
	if count := strings.Count(network, internal); count != 1 {
		t.Fatalf("edit target %q occurs %d times, want 1", internal, count)
	}
	network = strings.Replace(network, internal, session.entry()+internal, 1)
	if err := os.WriteFile(filepath.Join(directory, "network.json"), []byte(network), 0o600); err != nil {
		t.Fatal(err)
	}
}

type bgpUpstreamRuntime struct {
	tunnelRuntimeTopology
	upstream netns.NsHandle
	far      netns.NsHandle
}

func buildBGPUpstreamRuntime(t *testing.T, run *bgpRuntimeRun) bgpUpstreamRuntime {
	t.Helper()
	topology := bgpUpstreamRuntime{tunnelRuntimeTopology: buildTunnelRuntimeTopology(t, run.gateway)}
	topology.upstream = newTunnelRuntimeNamespace(t)
	topology.far = newTunnelRuntimeNamespace(t)
	for _, namespace := range []netns.NsHandle{topology.upstream, topology.far} {
		t.Cleanup(func() { _ = namespace.Close() })
	}
	linkTunnelRuntimeNamespaces(t, topology.endpoint, bgpUpstreamVPSDevice, []string{bgpUpstreamVPSAddress + "/64"},
		topology.upstream, bgpUpstreamDevice, []string{bgpUpstreamAddress + "/64"})
	linkTunnelRuntimeNamespaces(t, topology.upstream, "up-lan", []string{bgpUpstreamRemoteRouter + "/64"},
		topology.far, "far-host", []string{bgpUpstreamRemoteClient + "/64"})
	addRuntimeDefault(t, "far-host", bgpUpstreamRemoteRouter)
	setRuntimeNamespace(t, topology.upstream)
	writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
	addMappedRuntimeRoute(t, bgpUpstreamTunnelPrefix, bgpUpstreamVPSAddress, bgpUpstreamDevice)
	setRuntimeNamespace(t, run.gateway)
	run.topology = bgpRuntimeTopology{
		gateway: run.gateway, client: topology.client, isp: topology.isp,
		routers: map[string]netns.NsHandle{"vps": topology.endpoint, "upstream": topology.upstream},
		remotes: map[string]netns.NsHandle{"far": topology.far},
	}
	return topology
}

func requireBGPUpstreamExchange(t *testing.T, daemon *runtimeDaemon, topology bgpUpstreamRuntime) {
	t.Helper()
	client := netip.MustParseAddr(tunnelRuntimeClientV6)
	remoteClient := netip.MustParseAddr(bgpUpstreamRemoteClient)
	observed := waitTunnelRuntimeTransfer(t, daemon, topology.tunnelRuntimeTopology, topology.far, topology.client, "tcp6",
		net.JoinHostPort(remoteClient.String(), strconv.Itoa(bgpRuntimeRemotePort)), net.JoinHostPort(client.String(), "0"),
		tunnelRuntimeRecoverWindow)
	if observed != client {
		t.Fatalf("remote listener observed %s, want the internal client %s", observed, client)
	}
	observed = waitTunnelRuntimeTransfer(t, daemon, topology.tunnelRuntimeTopology, topology.client, topology.far, "tcp6",
		net.JoinHostPort(client.String(), strconv.Itoa(bgpRuntimeClientPort)), net.JoinHostPort(remoteClient.String(), "0"),
		tunnelRuntimeRecoverWindow)
	if observed != remoteClient {
		t.Fatalf("internal listener observed %s, want the remote client %s", observed, remoteClient)
	}
}

func requireBGPRuntimeIPv6Unchanged(t *testing.T, read func() string, device string, since time.Time) {
	t.Helper()
	requireBGPRuntimeFamily(t, read, device, "ready", "ready")
	entry, _ := readBGPRuntimeInterface(read, device)
	for _, transition := range entry.Ownership.Transitions {
		if transition.Family == "ipv6" && !transition.At.Before(since) {
			t.Fatalf("%s ipv6 changed during the fault of another session: %+v", device, transition)
		}
	}
}
