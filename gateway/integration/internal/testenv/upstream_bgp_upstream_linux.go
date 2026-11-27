//go:build linux && (firewall || protocol || systemd)

package testenv

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
	BgpUpstreamNetwork      = "2001:db8:50::/64"
	BgpUpstreamAddress      = "2001:db8:50::1"
	BgpUpstreamVPSAddress   = "2001:db8:50::2"
	BgpUpstreamRemoteRouter = "2001:db8:51::1"
	BgpUpstreamRemoteClient = "2001:db8:51::2"
	BgpUpstreamTunnelPrefix = "2001:db8:6::/64"
	BgpUpstreamASN          = 64514
	BgpUpstreamVPSDevice    = "far-up"
	BgpUpstreamDevice       = "up-vps"
)

type BgpTunnelRuntimeSession struct {
	Peer        string
	RemoteASN   uint32
	MultihopTTL int
	Routed      string
	Tier        int
}

func (s BgpTunnelRuntimeSession) Entry() string {
	multihop, routes := "", ""
	if s.MultihopTTL > 0 {
		multihop = fmt.Sprintf(`"multihop-ttl":%d,`, s.MultihopTTL)
	}
	if s.Routed != "" {
		routes = fmt.Sprintf(
			`"goodkind-mwan-steering:route":[{"destination":%q,"gateway":%q}],`,
			s.Routed,
			TunnelRuntimeInnerRemote,
		)
	}
	session := fmt.Sprintf(
		`"bgp-session":[{"name":"remote","peer-address":%q,"local-address":%q,%s`+
			`"local-as":%d,"remote-as":%d,"family":["ipv6-unicast"],"keepalive":%d,"hold":%d,"route-metric":300,`+
			`"import":[{"prefix":"::/0","min-length":0,"max-length":0}],`+
			`"export":[{"prefix":%q,"mode":"always","next-hop":%q}]}]`,
		s.Peer,
		TunnelRuntimeInnerLocal,
		multihop,
		BgpRuntimeLocalASN,
		s.RemoteASN,
		BgpRuntimeKeepalive,
		BgpRuntimeHold,
		BgpRuntimeHome,
		TunnelRuntimeInnerLocal,
	)
	return fmt.Sprintf(
		`{"name":%q,"type":"iana-if-type:tunnel",`+
			`"goodkind-mwan-steering:connection-id":"tunnel-6in4","goodkind-mwan-steering:owner":"mwan",`+
			`"goodkind-mwan-steering:link":{"mtu":%d,"tunnel":{"protocol":"6in4","underlay":%q,"remote-address":%q,`+
			`"local-address":%q,"ttl":64}},`+
			`"ietf-ip:ipv6":{"address":[{"ip":%q,"prefix-length":64}],%s`+
			`"goodkind-mwan-steering:translation":{"mode":"native"}},`+
			`"goodkind-mwan-steering:wan":{"name":"tunnel","table-id":200,"fw-mark":2,"fw-mark-prio":200,"from-prio":56,`+
			`"health":{"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":2,"recovery-threshold":1,`+
			`"check-interval":1,"targets-v6":[%q]},%s},`+
			`"goodkind-mwan-steering:steering":{"tier":%d,"weight":1}},`,
		TunnelRuntimeDevice,
		TunnelRuntimeMTU,
		TunnelRuntimeUnderlay,
		TunnelRuntimeRemote,
		TunnelRuntimeLocal,
		TunnelRuntimeInnerLocal,
		routes,
		BgpUpstreamRemoteClient,
		session,
		s.Tier,
	)
}

func WriteBGPTunnelRuntimeNetwork(
	t *testing.T,
	directory string,
	session BgpTunnelRuntimeSession,
	providers ...BgpRuntimeProvider,
) {
	t.Helper()
	internal := `{"name":"enmwanbr0","type":"iana-if-type:other"}`
	network := BgpRuntimeNetwork(providers...)
	if count := strings.Count(network, internal); count != 1 {
		t.Fatalf("edit target %q occurs %d times, want 1", internal, count)
	}
	network = strings.Replace(network, internal, session.Entry()+internal, 1)
	if err := os.WriteFile(
		filepath.Join(directory, "network.json"),
		[]byte(network),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

type BgpUpstreamRuntime struct {
	TunnelRuntimeTopology
	Upstream netns.NsHandle
	Far      netns.NsHandle
}

func BuildBGPUpstreamRuntime(t *testing.T, run *BgpRuntimeRun) BgpUpstreamRuntime {
	t.Helper()
	topology := BgpUpstreamRuntime{
		TunnelRuntimeTopology: BuildTunnelRuntimeTopology(t, run.Gateway),
	}
	topology.Upstream = NewTunnelRuntimeNamespace(t)
	topology.Far = NewTunnelRuntimeNamespace(t)
	for _, namespace := range []netns.NsHandle{topology.Upstream, topology.Far} {
		t.Cleanup(func() { _ = namespace.Close() })
	}
	LinkTunnelRuntimeNamespaces(
		t,
		topology.Endpoint,
		BgpUpstreamVPSDevice,
		[]string{BgpUpstreamVPSAddress + "/64"},
		topology.Upstream,
		BgpUpstreamDevice,
		[]string{BgpUpstreamAddress + "/64"},
	)
	LinkTunnelRuntimeNamespaces(
		t,
		topology.Upstream,
		"up-lan",
		[]string{BgpUpstreamRemoteRouter + "/64"},
		topology.Far,
		"far-host",
		[]string{BgpUpstreamRemoteClient + "/64"},
	)
	AddRuntimeDefault(t, "far-host", BgpUpstreamRemoteRouter)
	SetRuntimeNamespace(t, topology.Upstream)
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
	AddMappedRuntimeRoute(t, BgpUpstreamTunnelPrefix, BgpUpstreamVPSAddress, BgpUpstreamDevice)
	SetRuntimeNamespace(t, run.Gateway)
	run.Topology = BgpRuntimeTopology{
		Gateway: run.Gateway,
		Client:  topology.Client,
		Isp:     topology.Isp,
		Routers: map[string]netns.NsHandle{"vps": topology.Endpoint, "upstream": topology.Upstream},
		Remotes: map[string]netns.NsHandle{"far": topology.Far},
	}
	return topology
}

func RequireBGPUpstreamExchange(t *testing.T, daemon *RuntimeDaemon, topology BgpUpstreamRuntime) {
	t.Helper()
	client := netip.MustParseAddr(TunnelRuntimeClientV6)
	remoteClient := netip.MustParseAddr(BgpUpstreamRemoteClient)
	observed := WaitTunnelRuntimeTransfer(
		t,
		daemon,
		topology.TunnelRuntimeTopology,
		topology.Far,
		topology.Client,
		"tcp6",
		net.JoinHostPort(remoteClient.String(), strconv.Itoa(BgpRuntimeRemotePort)),
		net.JoinHostPort(client.String(), "0"),
		TunnelRuntimeRecoverWindow,
	)
	if observed != client {
		t.Fatalf("remote listener observed %s, want the internal client %s", observed, client)
	}
	observed = WaitTunnelRuntimeTransfer(
		t,
		daemon,
		topology.TunnelRuntimeTopology,
		topology.Client,
		topology.Far,
		"tcp6",
		net.JoinHostPort(client.String(), strconv.Itoa(BgpRuntimeClientPort)),
		net.JoinHostPort(remoteClient.String(), "0"),
		TunnelRuntimeRecoverWindow,
	)
	if observed != remoteClient {
		t.Fatalf("internal listener observed %s, want the remote client %s", observed, remoteClient)
	}
}

func RequireBGPRuntimeIPv6Unchanged(
	t *testing.T,
	read func() string,
	device string,
	since time.Time,
) {
	t.Helper()
	RequireBGPRuntimeFamily(t, read, device, "ready", "ready")
	entry, _ := ReadBGPRuntimeInterface(read, device)
	for _, transition := range entry.Ownership.Transitions {
		if transition.Family == "ipv6" && !transition.At.Before(since) {
			t.Fatalf("%s ipv6 changed during the fault of another session: %+v", device, transition)
		}
	}
}
