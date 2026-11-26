//go:build linux && (firewall || protocol || firewallnetns || systemd)

package commandline

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/gateway/internal/netif"
)

func writeStaticRuntimeNetwork(
	t *testing.T,
	directory string,
	ipv4 string,
	ipv6 string,
	gateway4 string,
	gateway6 string,
	metric int,
	prefixLength int,
	includeV4 bool,
) {
	t.Helper()
	writeOwnedRuntimeNetwork(t, directory, false, true)
	path := filepath.Join(directory, "network.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	var entries []map[string]json.RawMessage
	if err := json.Unmarshal(interfaces["interface"], &entries); err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		var name string
		if err := json.Unmarshal(entry["name"], &name); err != nil {
			t.Fatal(err)
		}
		if name != "owned397" {
			if name == "late397" {
				entry["ietf-ip:ipv4"] = json.RawMessage(
					`{"address":[{"ip":"10.39.8.1","prefix-length":24}]}`,
				)
			}
			continue
		}
		entry["enabled"] = json.RawMessage("true")
		if includeV4 {
			entry["ietf-ip:ipv4"] = json.RawMessage(
				fmt.Sprintf(
					`{"address":[{"ip":%q,"prefix-length":%d}],"goodkind-mwan-steering:dhcp":false,"goodkind-mwan-steering:translation":{"mode":"native"},"goodkind-mwan-steering:gateway":%q,"goodkind-mwan-steering:route-metric":%d,"goodkind-mwan-steering:route":[{"destination":"10.39.9.0/24"}]}`,
					ipv4,
					prefixLength,
					gateway4,
					metric,
				),
			)
		}
		entry["ietf-ip:ipv6"] = json.RawMessage(
			fmt.Sprintf(
				`{"address":[{"ip":%q,"prefix-length":64}],"goodkind-mwan-steering:dhcp":false,"goodkind-mwan-steering:translation":{"mode":"native"},"goodkind-mwan-steering:gateway":%q,"goodkind-mwan-steering:route-metric":%d,"goodkind-mwan-steering:route":[{"destination":"fd39:9::/64"}]}`,
				ipv6,
				gateway6,
				metric,
			),
		)
		entry["goodkind-mwan-steering:wan"] = json.RawMessage(
			`{"name":"static","table-id":397,"fw-mark":4,"fw-mark-prio":397,"from-prio":97}`,
		)
		entry["goodkind-mwan-steering:steering"] = json.RawMessage(`{"tier":0,"weight":1}`)
	}
	interfaces["interface"], err = json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitStaticRuntimeLog(t *testing.T, daemon *runtimeDaemon, expected string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(runtimeDaemonLog(t, daemon), expected) {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon log lacks %q: %s", expected, runtimeLogTail(t, daemon, 40))
}

func waitStaticRuntimeAddress(
	t *testing.T,
	daemon *runtimeDaemon,
	name string,
	prefix string,
	present bool,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var observed []netlink.Addr
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName(name)
		if netif.IsLinkNotFound(err) {
			assertRuntimeDaemonRunning(t, daemon)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, netlink.FAMILY_ALL)
		if err != nil {
			t.Fatal(err)
		}
		observed = addresses
		found := false
		for _, address := range addresses {
			if address.IPNet.String() == prefix {
				found = !present || address.Flags&unix.IFA_F_TENTATIVE == 0
			}
		}
		if found == present {
			return
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	var relevant []string
	for _, line := range strings.Split(runtimeDaemonLog(t, daemon), "\n") {
		if strings.Contains(line, `"module":"addresses"`) ||
			strings.Contains(line, `"module":"links"`) {
			relevant = append(relevant, line)
		}
	}
	if len(relevant) > 20 {
		relevant = relevant[len(relevant)-20:]
	}
	t.Fatalf(
		"address %s on %s present=%t not observed; current=%v: %s",
		prefix,
		name,
		present,
		observed,
		strings.Join(relevant, "\n"),
	)
}

func waitConfiguredRuntimeRoute(
	t *testing.T,
	daemon *runtimeDaemon,
	destination string,
) netlink.Route {
	t.Helper()
	metric := 0
	if strings.Contains(destination, ":") {
		metric = 1024
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		routes, err := netlink.RouteListFiltered(
			netlink.FAMILY_ALL,
			&netlink.Route{Table: unix.RT_TABLE_MAIN},
			netlink.RT_FILTER_TABLE,
		)
		if err != nil {
			t.Fatal(err)
		}
		for _, route := range routes {
			if route.Dst != nil && route.Dst.String() == destination &&
				route.Protocol == netif.OwnedStaticRouteProtocol &&
				route.Priority == metric {
				return route
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("configured route %s absent: %s", destination, runtimeLogTail(t, daemon, 40))
	return netlink.Route{}
}

func assertStaticRuntimePacket(
	t *testing.T,
	gateway netns.NsHandle,
	peer netns.NsHandle,
	network string,
	destination string,
) {
	t.Helper()
	setRuntimeNamespace(t, peer)
	listener, err := net.ListenPacket(network, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setRuntimeNamespace(t, gateway)
	connection, err := net.DialTimeout(network, destination, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("static-owned")); err != nil {
		t.Fatal(err)
	}
	setRuntimeNamespace(t, peer)
	if err := listener.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	count, _, err := listener.ReadFrom(buffer)
	setRuntimeNamespace(t, gateway)
	if err != nil || !bytes.Equal(buffer[:count], []byte("static-owned")) {
		t.Fatalf("%s packet: count=%d data=%q error=%v", network, count, buffer[:count], err)
	}
}
