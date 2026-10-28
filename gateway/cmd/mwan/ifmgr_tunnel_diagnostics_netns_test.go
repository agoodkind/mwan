//go:build linux && firewallnetns

package main

import (
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func logTunnelRuntimeKernelState(t *testing.T, label string) {
	t.Helper()
	rules, err := exec.Command("nft", "list", "ruleset").CombinedOutput()
	t.Logf("%s rules: %v: %s", label, err, rules)
	links, err := netlink.LinkList()
	t.Logf("%s link listing error: %v", label, err)
	for _, link := range links {
		attributes := link.Attrs()
		addresses, addressErr := netlink.AddrList(link, netlink.FAMILY_ALL)
		t.Logf("%s link %s index=%d type=%s flags=%s state=%s mtu=%d addresses=%v (%v) statistics=%+v",
			label, attributes.Name, attributes.Index, link.Type(), attributes.Flags, attributes.OperState, attributes.MTU,
			addresses, addressErr, attributes.Statistics)
	}
	for _, path := range []string{
		"/proc/sys/net/ipv6/conf/all/forwarding", "/proc/sys/net/ipv4/ip_forward", "/proc/net/snmp6", "/proc/net/snmp",
	} {
		value, readErr := os.ReadFile(path)
		t.Logf("%s %s: %v: %s", label, path, readErr, value)
	}
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		policy, ruleErr := netlink.RuleList(family)
		t.Logf("%s family %d policy rules: %v (%v)", label, family, policy, ruleErr)
		routes, routeErr := netlink.RouteListFiltered(family, &netlink.Route{Table: unix.RT_TABLE_UNSPEC}, netlink.RT_FILTER_TABLE)
		t.Logf("%s family %d route listing error: %v", label, family, routeErr)
		for _, route := range routes {
			if route.Table != unix.RT_TABLE_LOCAL {
				t.Logf("%s family %d route: %v", label, family, route)
			}
		}
	}
}

func tunnelRuntimeLog(t *testing.T, daemon *runtimeDaemon) string {
	t.Helper()
	var relevant []string
	for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
		if strings.Contains(line, `"level":"WARN"`) || strings.Contains(line, `"level":"ERROR"`) ||
			strings.Contains(line, "interface transition") || strings.Contains(line, "WAN state transition") {
			relevant = append(relevant, line)
		}
	}
	if len(relevant) > 80 {
		relevant = relevant[len(relevant)-80:]
	}
	return strings.Join(relevant, "\n")
}

func tunnelRuntimeInputRules(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("nft", "list", "chain", "inet", "filter", "input").CombinedOutput()
	if err != nil {
		t.Fatalf("read the input chain: %v: %s", err, output)
	}
	return string(output)
}

func tunnelRuntimePathMTU(t *testing.T, topology tunnelRuntimeTopology, namespace netns.NsHandle, destination string) int {
	t.Helper()
	setRuntimeNamespace(t, namespace)
	defer setRuntimeNamespace(t, topology.gateway)
	routes, err := netlink.RouteGet(net.ParseIP(destination))
	if err != nil || len(routes) != 1 {
		t.Fatalf("route to %s: %v, %v", destination, routes, err)
	}
	return routes[0].MTU
}

func tunnelRuntimeEndpointRoute(t *testing.T) (netlink.Route, bool) {
	t.Helper()
	routes, err := netlink.RouteListFiltered(unix.AF_INET, &netlink.Route{Table: tunnelRuntimeUnderlayTbl}, netlink.RT_FILTER_TABLE)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		if route.Dst != nil && route.Dst.String() == tunnelRuntimeRemote+"/32" {
			return route, true
		}
	}
	return netlink.Route{}, false
}

func waitTunnelRuntimeEndpointRoute(t *testing.T, daemon *runtimeDaemon, present bool) netlink.Route {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		route, found := tunnelRuntimeEndpointRoute(t)
		if found == present {
			return route
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("endpoint route present=%t not observed in table %d; daemon=%s", present, tunnelRuntimeUnderlayTbl, tunnelRuntimeLog(t, daemon))
	return netlink.Route{}
}
