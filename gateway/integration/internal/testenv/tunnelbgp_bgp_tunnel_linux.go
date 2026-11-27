//go:build linux && (firewall || protocol || systemd)

package testenv

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/vishvananda/netlink"
)

func BgpTunnelRuntimeNetwork(t *testing.T) string {
	t.Helper()
	targets := `"targets-v6":["` + TunnelRuntimeRemoteClient + `"]}`
	session := fmt.Sprintf(
		`,"bgp-session":[{"name":"endpoint","peer-address":%q,"local-address":%q,`+
			`"local-as":%d,"remote-as":%d,"family":["ipv6-unicast"],"keepalive":%d,"hold":%d,"route-metric":300,`+
			`"import":[{"prefix":"::/0","min-length":0,"max-length":0}],`+
			`"export":[{"prefix":%q,"mode":"always","next-hop":%q}]}]`,
		TunnelRuntimeInnerRemote,
		TunnelRuntimeInnerLocal,
		BgpRuntimeLocalASN,
		BgpRuntimeRemoteASN,
		BgpRuntimeKeepalive,
		BgpRuntimeHold,
		BgpRuntimeHome,
		TunnelRuntimeInnerLocal,
	)
	edits := map[string]string{
		`"goodkind-mwan-steering:gateway":"` + TunnelRuntimeInnerRemote + `",`: "",
		targets: targets + session,
	}
	network := TunnelRuntimeNetwork(true)
	for old, replacement := range edits {
		if count := strings.Count(network, old); count != 1 {
			t.Fatalf("edit target %q occurs %d times, want 1", old, count)
		}
		network = strings.Replace(network, old, replacement, 1)
	}
	return network
}

func RemoveTunnelRuntimeReturnRoute(t *testing.T, topology TunnelRuntimeTopology) {
	t.Helper()
	SetRuntimeNamespace(t, topology.Endpoint)
	defer SetRuntimeNamespace(t, topology.Gateway)
	link, err := netlink.LinkByName("far6in4")
	if err != nil {
		t.Fatal(err)
	}
	_, destination, err := net.ParseCIDR(BgpRuntimeHome)
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
