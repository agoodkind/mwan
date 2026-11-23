//go:build linux && (firewall || protocol || firewallnetns || systemd)

package commandline

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	mappedRuntimeChildEnv  = "MWAN_MAPPED_RUNTIME_TEST_CHILD"
	mappedRuntimeBinaryEnv = "MWAN_MAPPED_RUNTIME_TEST_BINARY"
	legacyRuntimeMAC       = "02:00:5e:00:53:20"
)

func waitMappedRuntimeRule(
	t *testing.T,
	daemon *runtimeDaemon,
	family string,
	table string,
	match string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		output, err := exec.Command("nft", "list", "table", family, table).CombinedOutput()
		if err == nil && strings.Contains(string(output), match) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	rules, _ := exec.Command("nft", "list", "ruleset").CombinedOutput()
	t.Fatalf(
		"%s %s did not install %s: rules=%s daemon=%s",
		family,
		table,
		match,
		rules,
		runtimeLogTail(t, daemon, 100),
	)
}

func addMappedRuntimeRoute(t *testing.T, destination, gateway, device string) {
	t.Helper()
	link, err := netlink.LinkByName(device)
	if err != nil {
		t.Fatal(err)
	}
	_, prefix, err := net.ParseCIDR(destination)
	if err != nil {
		t.Fatal(err)
	}
	route := &netlink.Route{LinkIndex: link.Attrs().Index, Dst: prefix}
	if gateway != "" {
		route.Gw = net.ParseIP(gateway)
	}
	if err := netlink.RouteAdd(route); err != nil {
		t.Fatal(err)
	}
}

type runtimePacketPhase struct {
	Stage   string
	Elapsed time.Duration
}

func assertMappedRuntimeReply(
	t *testing.T,
	daemon *runtimeDaemon,
	gateway netns.NsHandle,
	upstream netns.NsHandle,
	downstream netns.NsHandle,
	network string,
	external string,
	internal string,
) {
	t.Helper()
	started := time.Now()
	var phases []runtimePacketPhase
	mark := func(stage string) {
		phases = append(phases, runtimePacketPhase{Stage: stage, Elapsed: time.Since(started)})
	}
	var upstreamDeadline, downstreamDeadline time.Time
	defer func() {
		if !t.Failed() {
			return
		}
		t.Logf("mapped packet elapsed=%s phases=%+v", time.Since(started), phases)
		logMappedRuntimeDeadline(t, "upstream", started, upstreamDeadline)
		logMappedRuntimeDeadline(t, "downstream", started, downstreamDeadline)
		logMappedRuntimeNetwork(t, "downstream", downstream)
		logMappedRuntimeNetwork(t, "upstream", upstream)
		logMappedRuntimeNetwork(t, "gateway", gateway)
		output, err := exec.Command("nft", "list", "ruleset").CombinedOutput()
		t.Logf("mapped gateway rules: %v: %s", err, output)
		t.Logf("mapped gateway daemon: %s", runtimeLogTail(t, daemon, 80))
	}()
	setRuntimeNamespace(t, downstream)
	listener, err := net.ListenPacket(network, internal)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	mark("downstream_listener_bound")
	setRuntimeNamespace(t, upstream)
	connection, err := net.DialTimeout(network, external, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	mark("upstream_connected")
	upstreamDeadline = time.Now().Add(3 * time.Second)
	if err := connection.SetDeadline(upstreamDeadline); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte("mapped-request")); err != nil {
		t.Fatal(err)
	}
	mark("upstream_request_written")
	setRuntimeNamespace(t, downstream)
	downstreamDeadline = time.Now().Add(3 * time.Second)
	if err := listener.SetReadDeadline(downstreamDeadline); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 32)
	count, sender, err := listener.ReadFrom(buffer)
	mark("downstream_request_read")
	if err != nil || string(buffer[:count]) != "mapped-request" {
		t.Fatalf(
			"mapped request: count=%d data=%q error=%v: %s",
			count,
			buffer[:count],
			err,
			runtimeLogTail(t, daemon, 50),
		)
	}
	if _, err := listener.WriteTo([]byte("mapped-reply"), sender); err != nil {
		t.Fatal(err)
	}
	mark("downstream_reply_written")
	setRuntimeNamespace(t, upstream)
	count, err = connection.Read(buffer)
	mark("upstream_reply_read")
	setRuntimeNamespace(t, gateway)
	if err != nil || string(buffer[:count]) != "mapped-reply" {
		t.Fatalf("mapped reply: count=%d data=%q error=%v", count, buffer[:count], err)
	}
}

func logMappedRuntimeDeadline(t *testing.T, scope string, started, deadline time.Time) {
	t.Helper()
	if !deadline.IsZero() {
		t.Logf(
			"%s deadline elapsed=%s remaining=%s",
			scope,
			deadline.Sub(started),
			time.Until(deadline),
		)
	}
}

func logMappedRuntimeNetwork(t *testing.T, scope string, namespace netns.NsHandle) {
	t.Helper()
	setRuntimeNamespace(t, namespace)
	routes, routeErr := netlink.RouteListFiltered(
		unix.AF_INET6,
		&netlink.Route{Table: unix.RT_TABLE_UNSPEC},
		netlink.RT_FILTER_TABLE,
	)
	rules, ruleErr := netlink.RuleList(unix.AF_INET6)
	neighbors, neighborErr := netlink.NeighList(0, unix.AF_INET6)
	addresses, addressErr := netlink.AddrList(nil, unix.AF_INET6)
	t.Logf(
		"%s IPv6 routes=%+v error=%v rules=%+v error=%v neighbors=%+v error=%v addresses_error=%v",
		scope,
		routes,
		routeErr,
		rules,
		ruleErr,
		neighbors,
		neighborErr,
		addressErr,
	)
	for _, address := range addresses {
		t.Logf(
			"%s IPv6 address=%s link=%d flags=%d preferred=%d valid=%d",
			scope,
			address.String(),
			address.LinkIndex,
			address.Flags,
			address.PreferedLft,
			address.ValidLft,
		)
	}
}

func assertMappedRuntimeRuleAbsent(t *testing.T, family, table, match string) {
	t.Helper()
	output, err := exec.Command("nft", "list", "table", family, table).CombinedOutput()
	if err != nil {
		t.Fatalf("read %s %s rules: %v: %s", family, table, err, output)
	}
	if strings.Contains(string(output), match) {
		t.Fatalf("%s %s unexpectedly contains %s: %s", family, table, match, output)
	}
}

func mappedRuntimeHasLine(output string, parts []string) bool {
	for _, line := range strings.Split(output, "\n") {
		matched := true
		for _, part := range parts {
			if !strings.Contains(line, part) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func writeMappedRuntimeProvider(t *testing.T, directory, externalV4, externalV6 string) {
	t.Helper()
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
		if string(entry["name"]) == `"enwebpass0"` {
			entry["goodkind-mwan-steering:connection-id"] = json.RawMessage(`"webpass"`)
			delete(entry, "goodkind-mwan-steering:link-files")
			entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"external"`)
			delete(entry, "goodkind-mwan-steering:link")
			entry["ietf-ip:ipv4"] = json.RawMessage(
				`{"address":[{"ip":"10.20.0.1","prefix-length":24}],"goodkind-mwan-steering:gateway":"10.20.0.2","goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44","static-mapping":[{"external":"10.20.0.3","internal":"192.0.2.3"},{"external":"10.20.0.4","internal":"192.0.2.4","delivery":"local"},{"external":"10.20.0.5","internal":"192.0.2.4","delivery":"routed"}]}}`,
			)
			entry["goodkind-mwan-steering:wan"] = json.RawMessage(
				`{"name":"webpass","table-id":200,"fw-mark":2,"fw-mark-prio":200,"from-prio":56,"health":{"enabled":false}}`,
			)
			entry["goodkind-mwan-steering:steering"] = json.RawMessage(`{"tier":1,"weight":1}`)
			continue
		}
		if string(entry["name"]) != `"owned397"` {
			continue
		}
		entry["ietf-ip:ipv4"] = json.RawMessage(
			fmt.Sprintf(
				`{"address":[{"ip":"10.39.7.1","prefix-length":24}],"goodkind-mwan-steering:gateway":"10.39.7.2","goodkind-mwan-steering:route-metric":398,"goodkind-mwan-steering:translation":{"mode":"ietf-nat:napt44","static-mapping":[{"external":%q,"internal":"192.0.2.3","delivery":"local"},{"external":"10.39.7.4","internal":"192.0.2.4","delivery":"routed"}]}}`,
				externalV4,
			),
		)
		entry["ietf-ip:ipv6"] = json.RawMessage(
			fmt.Sprintf(
				`{"address":[{"ip":"fd39:7::1","prefix-length":64}],"goodkind-mwan-steering:gateway":"fd39:7::2","goodkind-mwan-steering:route-metric":398,"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"configured","external-prefix":%q}}}`,
				externalV6,
			),
		)
		entry["goodkind-mwan-steering:wan"] = json.RawMessage(
			`{"name":"owned","table-id":398,"fw-mark":4,"fw-mark-prio":398,"from-prio":58}`,
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
