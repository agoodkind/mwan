//go:build linux && firewallnetns

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func tunnelRuntimeNetwork(withTunnel bool) string {
	probe := `"enabled":true,"ping-count":1,"success-threshold":1,"failure-threshold":2,"recovery-threshold":1,"check-interval":1`
	entries := []string{
		`{"name":"` + tunnelRuntimeUnderlay + `","type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",` +
			`"ietf-ip:ipv4":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44"}},` +
			`"goodkind-mwan-steering:wan":{"name":"isp","table-id":100,"fw-mark":1,"fw-mark-prio":100,"from-prio":55,` +
			`"health":{` + probe + `,"targets-v4":["` + tunnelRuntimeISPGateway + `"],"targets-v6":[]}},` +
			`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`,
		`{"name":"` + tunnelRuntimeAlternate + `","type":"iana-if-type:other","goodkind-mwan-steering:owner":"external",` +
			`"ietf-ip:ipv4":{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44"}},` +
			`"goodkind-mwan-steering:wan":{"name":"alt","table-id":300,"fw-mark":3,"fw-mark-prio":300,"from-prio":57,` +
			`"health":{` + probe + `,"targets-v4":["` + tunnelRuntimeAltGateway + `"],"targets-v6":[]}},` +
			`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`,
	}
	if withTunnel {
		entries = append(entries,
			`{"name":"`+tunnelRuntimeDevice+`","type":"iana-if-type:tunnel",`+
				`"goodkind-mwan-steering:connection-id":"tunnel-6in4","goodkind-mwan-steering:owner":"mwan",`+
				`"goodkind-mwan-steering:link":{"mtu":`+fmt.Sprint(tunnelRuntimeMTU)+`,"tunnel":{"protocol":"6in4",`+
				`"underlay":"`+tunnelRuntimeUnderlay+`","remote-address":"`+tunnelRuntimeRemote+`",`+
				`"local-address":"`+tunnelRuntimeLocal+`","ttl":64}},`+
				`"ietf-ip:ipv6":{"address":[{"ip":"`+tunnelRuntimeInnerLocal+`","prefix-length":64}],`+
				`"goodkind-mwan-steering:gateway":"`+tunnelRuntimeInnerRemote+`",`+
				`"goodkind-mwan-steering:translation":{"mode":"native"}},`+
				`"goodkind-mwan-steering:wan":{"name":"tunnel","table-id":200,"fw-mark":2,"fw-mark-prio":200,"from-prio":56,`+
				`"health":{`+probe+`,"targets-v6":["`+tunnelRuntimeRemoteClient+`"]}},`+
				`"goodkind-mwan-steering:steering":{"tier":0,"weight":1}}`)
	}
	entries = append(entries,
		`{"name":"enmwanbr0","type":"iana-if-type:other"}`,
		`{"name":"enmgmt0","type":"iana-if-type:other"}`)
	return `{"ietf-interfaces:interfaces":{"interface":[` + strings.Join(entries, ",") + `],` +
		`"goodkind-mwan-steering:steering-group":{"hash-mode":"source","reserved-tables":[400,500],` +
		`"translation":{"internal-prefix":"2001:db8:b01::/60","opnsense-edge-v6":"` + tunnelRuntimeClientV6 + `",` +
		`"mwanbr-edge-v6":"2001:db8:b01:fe::3"},` +
		`"routes":{"internal-iface":"enmwanbr0","internal-net-v4":"192.0.2.0/29"},` +
		`"firewall":{"management-interface":"enmgmt0","management-service":[{"protocol":"tcp","port":22}],` +
		`"pinned-set-v4-name":"isp_pinned_v4","pinned-set-v6-name":"isp_pinned_v6"},` +
		`"health":{"probe-timeout":500}}}}`
}

func writeTunnelRuntimeNetwork(t *testing.T, directory string, withTunnel bool) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(directory, "network.json"), []byte(tunnelRuntimeNetwork(withTunnel)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func setTunnelRuntimeFault(t *testing.T, topology tunnelRuntimeTopology, dropped bool) {
	t.Helper()
	setRuntimeNamespace(t, topology.endpoint)
	if dropped {
		runRuntimeNFT(t, "add", "table", "ip", tunnelRuntimeFaultTable)
		runRuntimeNFT(t, "add", "chain", "ip", tunnelRuntimeFaultTable, "input", "{ type filter hook input priority 0; }")
		runRuntimeNFT(t, "add", "rule", "ip", tunnelRuntimeFaultTable, "input", "ip", "protocol", fmt.Sprint(tunnelRuntimeProtocol41), "drop")
	} else {
		runRuntimeNFT(t, "delete", "table", "ip", tunnelRuntimeFaultTable)
	}
	setRuntimeNamespace(t, topology.gateway)
}

func setTunnelRuntimeISPLink(t *testing.T, topology tunnelRuntimeTopology, up bool) {
	t.Helper()
	setRuntimeNamespace(t, topology.isp)
	link, err := netlink.LinkByName(tunnelRuntimeISPLink)
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
	setRuntimeNamespace(t, topology.gateway)
}
