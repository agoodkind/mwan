//go:build linux && (firewall || protocol || firewallnetns)

package commandline

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func bgpTunnelRuntimeNetwork(t *testing.T) string {
	t.Helper()
	targets := `"targets-v6":["` + tunnelRuntimeRemoteClient + `"]}`
	session := fmt.Sprintf(
		`,"bgp-session":[{"name":"endpoint","peer-address":%q,"local-address":%q,`+
			`"local-as":%d,"remote-as":%d,"family":["ipv6-unicast"],"keepalive":%d,"hold":%d,"route-metric":300,`+
			`"import":[{"prefix":"::/0","min-length":0,"max-length":0}],`+
			`"export":[{"prefix":%q,"mode":"always","next-hop":%q}]}]`,
		tunnelRuntimeInnerRemote,
		tunnelRuntimeInnerLocal,
		bgpRuntimeLocalASN,
		bgpRuntimeRemoteASN,
		bgpRuntimeKeepalive,
		bgpRuntimeHold,
		bgpRuntimeHome,
		tunnelRuntimeInnerLocal,
	)
	edits := map[string]string{
		`"goodkind-mwan-steering:gateway":"` + tunnelRuntimeInnerRemote + `",`: "",
		targets: targets + session,
	}
	network := tunnelRuntimeNetwork(true)
	for old, replacement := range edits {
		if count := strings.Count(network, old); count != 1 {
			t.Fatalf("edit target %q occurs %d times, want 1", old, count)
		}
		network = strings.Replace(network, old, replacement, 1)
	}
	return network
}

func removeTunnelRuntimeReturnRoute(t *testing.T, topology tunnelRuntimeTopology) {
	t.Helper()
	setRuntimeNamespace(t, topology.endpoint)
	defer setRuntimeNamespace(t, topology.gateway)
	link, err := netlink.LinkByName("far6in4")
	if err != nil {
		t.Fatal(err)
	}
	_, destination, err := net.ParseCIDR(bgpRuntimeHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteDel(&netlink.Route{
		Dst:       destination,
		LinkIndex: link.Attrs().Index,
	}); err != nil {
		t.Fatalf("remove the configured return route: %v", err)
	}
}
