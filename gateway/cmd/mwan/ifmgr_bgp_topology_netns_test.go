//go:build linux && firewallnetns

package main

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
	bgpRuntimeHome         = "2001:db8:b01:fe::/64"
	bgpRuntimeUndeclared   = "2001:db8:77::/48"
	bgpRuntimeFaultTable   = "bgpfault"
	bgpRuntimeLocalASN     = 64512
	bgpRuntimeRemoteASN    = 64513
	bgpRuntimeFromPrioBase = 60
)

type bgpRuntimeProvider struct {
	id, device, routerDevice string
	network                  string
	table, mark, tier        int
	metric                   int
	localASN, remoteASN      uint32
	learned                  netip.Prefix
	exports                  []bgpRuntimeExport
	session                  bool
}

type bgpRuntimeExport struct {
	prefix, backupFor string
}

func (p bgpRuntimeProvider) exportEntries() string {
	entries := make([]string, 0, len(p.exports))
	for _, export := range p.exports {
		mode := `"mode":"always",`
		if export.backupFor != "" {
			mode = `"mode":"backup","backup-for":["` + export.backupFor + `"],`
		}
		entries = append(entries, fmt.Sprintf(`{"prefix":%q,%s"next-hop":%q}`, export.prefix, mode, p.local()))
	}
	return strings.Join(entries, ",")
}

func (p bgpRuntimeProvider) local() netip.Addr { return netip.MustParseAddr(p.network + ":179::2") }

func (p bgpRuntimeProvider) peer() netip.Addr { return netip.MustParseAddr(p.network + ":179::1") }

func (p bgpRuntimeProvider) remoteClient() string { return p.network + ":99::2" }

func (p bgpRuntimeProvider) entry() string {
	probe := `"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":2,"recovery-threshold":1,"check-interval":1`
	sessions := ""
	if p.session {
		sessions = fmt.Sprintf(`,"bgp-session":[{"name":"upstream","peer-address":%q,"local-address":%q,`+
			`"local-as":%d,"remote-as":%d,"router-id":"192.0.2.1","keepalive":%d,"hold":%d,"route-metric":%d,`+
			`"import":[{"prefix":%q,"min-length":%d,"max-length":%d}],`+
			`"export":[%s]}]`,
			p.peer(), p.local(), p.localASN, p.remoteASN, bgpRuntimeKeepalive, bgpRuntimeHold, p.metric,
			p.learned, p.learned.Bits(), p.learned.Bits(), p.exportEntries())
	}
	return fmt.Sprintf(`{"name":%q,"type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",`+
		`"ietf-ip:ipv6":{"goodkind-mwan-steering:translation":{"mode":"native"}},`+
		`"goodkind-mwan-steering:wan":{"name":%q,"table-id":%d,"fw-mark":%d,"fw-mark-prio":%d,"from-prio":%d,`+
		`"health":{%s,"targets-v4":[],"targets-v6":[%q]}%s},`+
		`"goodkind-mwan-steering:steering":{"tier":%d,"weight":1}}`,
		p.device, p.id, p.table, p.mark, p.table, bgpRuntimeFromPrioBase+p.mark, probe, p.remoteClient(), sessions, p.tier)
}

func bgpRuntimeNetwork(providers ...bgpRuntimeProvider) string {
	entries := []string{
		`{"name":"` + tunnelRuntimeUnderlay + `","type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",` +
			`"ietf-ip:ipv4":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44"}},` +
			`"goodkind-mwan-steering:wan":{"name":"isp","table-id":100,"fw-mark":1,"fw-mark-prio":100,"from-prio":55,` +
			`"health":{"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":2,"recovery-threshold":1,` +
			`"check-interval":1,"targets-v4":["` + tunnelRuntimeISPGateway + `"],"targets-v6":[]}},` +
			`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`,
	}
	for _, provider := range providers {
		entries = append(entries, provider.entry())
	}
	entries = append(entries, `{"name":"enmwanbr0","type":"iana-if-type:other"}`, `{"name":"enmgmt0","type":"iana-if-type:other"}`)
	return `{"ietf-interfaces:interfaces":{"interface":[` + strings.Join(entries, ",") + `],` +
		`"goodkind-mwan-steering:steering-group":{"hash-mode":"source","reserved-tables":[400,500],` +
		`"translation":{"internal-prefix":"2001:db8:b01::/60","opnsense-edge-v6":"` + tunnelRuntimeClientV6 + `",` +
		`"mwanbr-edge-v6":"2001:db8:b01:fe::3"},` +
		`"routes":{"internal-iface":"enmwanbr0","internal-net-v4":"192.0.2.0/29"},` +
		`"firewall":{"management-interface":"enmgmt0","management-service":[{"protocol":"tcp","port":22}],` +
		`"pinned-set-v4-name":"isp_pinned_v4","pinned-set-v6-name":"isp_pinned_v6"},` +
		`"health":{"probe-timeout":500}}}}`
}

func writeBGPRuntimeNetwork(t *testing.T, directory string, providers ...bgpRuntimeProvider) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "network.json"), []byte(bgpRuntimeNetwork(providers...)), 0o600); err != nil {
		t.Fatal(err)
	}
}

type bgpRuntimeTopology struct {
	gateway netns.NsHandle
	client  netns.NsHandle
	isp     netns.NsHandle
	routers map[string]netns.NsHandle
	remotes map[string]netns.NsHandle
}

func buildBGPRuntimeTopology(t *testing.T, gateway netns.NsHandle, providers ...bgpRuntimeProvider) bgpRuntimeTopology {
	t.Helper()
	management := newRuntimePeer(t, gateway, "enmgmt0", "mgmt-host", []string{"192.0.2.65/29"}, []string{"192.0.2.66/29"}, "")
	client := newRuntimePeer(t, gateway, "enmwanbr0", "lan-host", []string{"192.0.2.1/29", "2001:db8:b01:fe::3/64"},
		[]string{tunnelRuntimeClientV6 + "/64", tunnelRuntimeClientV4 + "/29"}, "")
	isp := newRuntimePeer(t, gateway, tunnelRuntimeUnderlay, tunnelRuntimeISPLink,
		[]string{tunnelRuntimeLocal + "/30"}, []string{tunnelRuntimeISPGateway + "/30"}, "")
	topology := bgpRuntimeTopology{
		gateway: gateway, client: client.namespace, isp: isp.namespace,
		routers: make(map[string]netns.NsHandle), remotes: make(map[string]netns.NsHandle),
	}
	namespaces := []netns.NsHandle{management.namespace, client.namespace, isp.namespace}
	for _, provider := range providers {
		router := newRuntimePeer(t, gateway, provider.device, provider.routerDevice,
			[]string{provider.local().String() + "/64"}, []string{provider.peer().String() + "/64"}, "")
		remote := newTunnelRuntimeNamespace(t)
		linkTunnelRuntimeNamespaces(t, router.namespace, provider.routerDevice+"-far", []string{provider.network + ":99::1/64"},
			remote, "remote-host", []string{provider.remoteClient() + "/64"})
		addRuntimeDefault(t, "remote-host", provider.network+":99::1")
		setRuntimeNamespace(t, router.namespace)
		writeTunnelRuntimeSetting(t, "/proc/sys/net/ipv6/conf/all/forwarding", "1")
		topology.routers[provider.id], topology.remotes[provider.id] = router.namespace, remote
		namespaces = append(namespaces, router.namespace, remote)
	}
	for _, namespace := range namespaces {
		t.Cleanup(func() { _ = namespace.Close() })
	}
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
