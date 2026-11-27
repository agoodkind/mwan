//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netns"
)

const (
	BgpRuntimeHome         = "2001:db8:b01:fe::/64"
	BgpRuntimeUndeclared   = "2001:db8:77::/48"
	BgpRuntimeFaultTable   = "bgpfault"
	BgpRuntimeLocalASN     = 64512
	BgpRuntimeRemoteASN    = 64513
	BgpRuntimeFromPrioBase = 60
)

type BgpRuntimeProvider struct {
	Id, Device, RouterDevice string
	Network                  string
	Table, Mark, Tier        int
	Metric                   int
	LocalASN, RemoteASN      uint32
	Learned                  netip.Prefix
	Exports                  []BgpRuntimeExport
	Session                  bool
	RestartTime              int
}

type BgpRuntimeExport struct {
	Prefix, BackupFor string
}

func (p BgpRuntimeProvider) ExportEntries() string {
	entries := make([]string, 0, len(p.Exports))
	for _, export := range p.Exports {
		mode := `"mode":"always",`
		if export.BackupFor != "" {
			mode = `"mode":"backup","backup-for":["` + export.BackupFor + `"],`
		}
		entries = append(
			entries,
			fmt.Sprintf(`{"prefix":%q,%s"next-hop":%q}`, export.Prefix, mode, p.Local()),
		)
	}
	return strings.Join(entries, ",")
}

func (p BgpRuntimeProvider) Local() netip.Addr { return netip.MustParseAddr(p.Network + ":179::2") }

func (p BgpRuntimeProvider) Peer() netip.Addr { return netip.MustParseAddr(p.Network + ":179::1") }

func (p BgpRuntimeProvider) RemoteClient() string { return p.Network + ":99::2" }

func (p BgpRuntimeProvider) Entry() string {
	probe := `"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":1,"recovery-threshold":1,"check-interval":1`
	sessions := ""
	if p.Session {
		restart := ""
		if p.RestartTime > 0 {
			restart = fmt.Sprintf(`"graceful-restart-time":%d,`, p.RestartTime)
		}
		sessions = fmt.Sprintf(
			`,"bgp-session":[{"name":"upstream","peer-address":%q,"local-address":%q,`+
				`"local-as":%d,"remote-as":%d,"family":["ipv6-unicast"],"keepalive":%d,"hold":%d,%s"route-metric":%d,`+
				`"import":[{"prefix":%q,"min-length":%d,"max-length":%d}],`+
				`"export":[%s]}]`,
			p.Peer(),
			p.Local(),
			p.LocalASN,
			p.RemoteASN,
			BgpRuntimeKeepalive,
			BgpRuntimeHold,
			restart,
			p.Metric,
			p.Learned,
			p.Learned.Bits(),
			p.Learned.Bits(),
			p.ExportEntries(),
		)
	}
	return fmt.Sprintf(
		`{"name":%q,"type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",`+
			`"ietf-ip:ipv6":{"goodkind-mwan-steering:translation":{"mode":"native"}},`+
			`"goodkind-mwan-steering:wan":{"name":%q,"table-id":%d,"fw-mark":%d,"fw-mark-prio":%d,"from-prio":%d,`+
			`"health":{%s,"targets-v4":[],"targets-v6":[%q]}%s},`+
			`"goodkind-mwan-steering:steering":{"tier":%d,"weight":1}}`,
		p.Device,
		p.Id,
		p.Table,
		p.Mark,
		p.Table,
		BgpRuntimeFromPrioBase+p.Mark,
		probe,
		p.RemoteClient(),
		sessions,
		p.Tier,
	)
}

func BgpRuntimeNetwork(providers ...BgpRuntimeProvider) string {
	entries := []string{
		`{"name":"` + TunnelRuntimeUnderlay + `","type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",` +
			`"ietf-ip:ipv4":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44"}},` +
			`"goodkind-mwan-steering:wan":{"name":"isp","table-id":100,"fw-mark":1,"fw-mark-prio":100,"from-prio":55,` +
			`"health":{"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":2,"recovery-threshold":1,` +
			`"check-interval":1,"targets-v4":["` + TunnelRuntimeISPGateway + `"],"targets-v6":[]}},` +
			`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`,
	}
	for _, provider := range providers {
		entries = append(entries, provider.Entry())
	}
	entries = append(
		entries,
		`{"name":"enmwanbr0","type":"iana-if-type:other"}`,
		`{"name":"enmgmt0","type":"iana-if-type:other"}`,
	)
	return `{"ietf-interfaces:interfaces":{"interface":[` + strings.Join(entries, ",") + `],` +
		`"goodkind-mwan-steering:steering-group":{"hash-mode":"source","reserved-tables":[400],` +
		`"translation":{"internal-prefix":"2001:db8:b01::/60","opnsense-edge-v6":"` + TunnelRuntimeClientV6 + `",` +
		`"mwanbr-edge-v6":"2001:db8:b01:fe::3"},` +
		`"routes":{"internal-iface":"enmwanbr0","internal-net-v4":"192.0.2.0/29"},` +
		`"firewall":{"management-interface":"enmgmt0","management-service":[{"protocol":"tcp","port":22}],` +
		`"pinned-set-v4-name":"isp_pinned_v4","pinned-set-v6-name":"isp_pinned_v6"},` +
		`"health":{"probe-timeout":500}}}}`
}

func WriteBGPRuntimeNetwork(t *testing.T, directory string, providers ...BgpRuntimeProvider) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(directory, "network.json"),
		[]byte(BgpRuntimeNetwork(providers...)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

type BgpRuntimeTopology struct {
	Gateway netns.NsHandle
	Client  netns.NsHandle
	Isp     netns.NsHandle
	Routers map[string]netns.NsHandle
	Remotes map[string]netns.NsHandle
}

func AddBGPRuntimeProvider(
	t *testing.T,
	gateway netns.NsHandle,
	provider BgpRuntimeProvider,
) (router, remote netns.NsHandle) {
	t.Helper()
	peer := NewRuntimePeer(
		t,
		gateway,
		provider.Device,
		provider.RouterDevice,
		[]string{provider.Local().String() + "/64"},
		[]string{provider.Peer().String() + "/64"},
		"",
	)
	remote = NewTunnelRuntimeNamespace(t)
	LinkTunnelRuntimeNamespaces(
		t,
		peer.Namespace,
		provider.RouterDevice+"-far",
		[]string{provider.Network + ":99::1/64"},
		remote,
		"remote-host",
		[]string{provider.RemoteClient() + "/64"},
	)
	AddRuntimeDefault(t, "remote-host", provider.Network+":99::1")
	SetRuntimeNamespace(t, peer.Namespace)
	WriteTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
	return peer.Namespace, remote
}

func BuildBGPRuntimeTopology(
	t *testing.T,
	gateway netns.NsHandle,
	providers ...BgpRuntimeProvider,
) BgpRuntimeTopology {
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
	client := NewRuntimePeer(
		t,
		gateway,
		"enmwanbr0",
		"lan-host",
		[]string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"},
		[]string{TunnelRuntimeClientV6 + "/64", TunnelRuntimeClientV4 + "/29"},
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
	topology := BgpRuntimeTopology{
		Gateway: gateway,
		Client:  client.Namespace,
		Isp:     isp.Namespace,
		Routers: make(map[string]netns.NsHandle),
		Remotes: make(map[string]netns.NsHandle),
	}
	namespaces := []netns.NsHandle{management.Namespace, client.Namespace, isp.Namespace}
	for _, provider := range providers {
		router, remote := AddBGPRuntimeProvider(t, gateway, provider)
		topology.Routers[provider.Id], topology.Remotes[provider.Id] = router, remote
		namespaces = append(namespaces, router, remote)
	}
	for _, namespace := range namespaces {
		t.Cleanup(func() { _ = namespace.Close() })
	}
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
