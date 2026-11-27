//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func TunnelRuntimeNetwork(withTunnel bool) string {
	probe := `"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":2,"recovery-threshold":1,"check-interval":1`
	entries := []string{
		`{"name":"` + TunnelRuntimeUnderlay + `","type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",` +
			`"ietf-ip:ipv4":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44"}},` +
			`"goodkind-mwan-steering:wan":{"name":"isp","table-id":100,"fw-mark":1,"fw-mark-prio":100,"from-prio":55,` +
			`"health":{` + probe + `,"targets-v4":["` + TunnelRuntimeISPGateway + `"],"targets-v6":[]}},` +
			`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`,
		`{"name":"` + TunnelRuntimeAlternate + `","type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",` +
			`"ietf-ip:ipv4":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44"}},` +
			`"goodkind-mwan-steering:wan":{"name":"alt","table-id":300,"fw-mark":3,"fw-mark-prio":300,"from-prio":57,` +
			`"health":{` + probe + `,"targets-v4":["` + TunnelRuntimeAltGateway + `"],"targets-v6":[]}},` +
			`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`,
	}
	if withTunnel {
		entries = append(
			entries,
			`{"name":"`+TunnelRuntimeDevice+`","type":"iana-if-type:tunnel",`+
				`"goodkind-mwan-steering:connection-id":"tunnel-6in4","goodkind-mwan-steering:owner":"mwan",`+
				`"goodkind-mwan-steering:link":{"mtu":`+fmt.Sprint(
				TunnelRuntimeMTU,
			)+`,"tunnel":{"protocol":"6in4",`+
				`"underlay":"`+TunnelRuntimeUnderlay+`","remote-address":"`+TunnelRuntimeRemote+`",`+
				`"local-address":"`+TunnelRuntimeLocal+`","ttl":64}},`+
				`"ietf-ip:ipv6":{"address":[{"ip":"`+TunnelRuntimeInnerLocal+`","prefix-length":64}],`+
				`"goodkind-mwan-steering:gateway":"`+TunnelRuntimeInnerRemote+`",`+
				`"goodkind-mwan-steering:translation":{"mode":"native"}},`+
				`"goodkind-mwan-steering:wan":{"name":"tunnel","table-id":200,"fw-mark":2,"fw-mark-prio":200,"from-prio":56,`+
				`"health":{`+probe+`,"targets-v6":["`+TunnelRuntimeRemoteClient+`"]}},`+
				`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`,
		)
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

func WriteTunnelRuntimeNetwork(t *testing.T, directory string, withTunnel bool) {
	t.Helper()
	if err := os.WriteFile(
		filepath.Join(directory, "network.json"),
		[]byte(TunnelRuntimeNetwork(withTunnel)),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func SetTunnelRuntimeFault(t *testing.T, topology TunnelRuntimeTopology, dropped bool) {
	t.Helper()
	SetRuntimeNamespace(t, topology.Endpoint)
	if dropped {
		RunRuntimeNFT(t, "add", "table", "ip", TunnelRuntimeFaultTable)
		RunRuntimeNFT(
			t,
			"add",
			"chain",
			"ip",
			TunnelRuntimeFaultTable,
			"input",
			"{ type filter hook input priority 0; }",
		)
		RunRuntimeNFT(
			t,
			"add",
			"rule",
			"ip",
			TunnelRuntimeFaultTable,
			"input",
			"ip",
			"protocol",
			fmt.Sprint(TunnelRuntimeProtocol41),
			"drop",
		)
	} else {
		RunRuntimeNFT(t, "delete", "table", "ip", TunnelRuntimeFaultTable)
	}
	SetRuntimeNamespace(t, topology.Gateway)
}

func SetTunnelRuntimeISPLink(t *testing.T, topology TunnelRuntimeTopology, up bool) {
	t.Helper()
	SetRuntimeNamespace(t, topology.Isp)
	link, err := netlink.LinkByName(TunnelRuntimeISPLink)
	if err != nil {
		t.Fatal(err)
	}
	if up {
		err = netlink.LinkSetUp(link)
	} else {
		err = netlink.LinkSetDown(link)
	}
	if err != nil {
		t.Fatal(err)
	}
	SetRuntimeNamespace(t, topology.Gateway)
}
