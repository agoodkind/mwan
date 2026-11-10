//go:build linux && firewallnetns

package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/netif"
)

func TestDebugInspectionPublicNamespace(t *testing.T) {
	runDebugProbeNamespaceTest(t, runDebugInspectionPublicNamespace)
}

func runDebugInspectionPublicNamespace(t *testing.T, binary string) {
	root := t.TempDir()
	networkDir := filepath.Join(root, "mwan")
	if err := os.Mkdir(networkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../yang/instances/network-min.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(fixture, []byte(`"name": "enatt0",`), []byte(`"name": "enatt0", "goodkind-mwan-steering:connection-id": "att-line",`))
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, networkDir, "/etc/mwan")
	schema, err := filepath.Abs("../../internal/yangpub/schema")
	if err != nil {
		t.Fatal(err)
	}
	bindDebugProbePath(t, schema, "/usr/local/share/wanconfig/yang")
	configPath := filepath.Join(root, "config.toml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for index, name := range []string{"enatt0", "enmbrains0", "enwebpass0", "enmwanbr0"} {
		var link netlink.Link = &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}
		if name == "enmbrains0" {
			link = &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: name}, PeerName: "inspection-peer"}
		}
		if err := netlink.LinkAdd(link); err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
		for _, cidr := range []string{fmt.Sprintf("198.51.100.%d/32", index+1), fmt.Sprintf("2001:db8:%d::1/64", index+1)} {
			address, err := netlink.ParseAddr(cidr)
			if err != nil {
				t.Fatal(err)
			}
			address.Flags = unix.IFA_F_NODAD
			if err := netlink.AddrAdd(link, address); err != nil {
				t.Fatal(err)
			}
		}
		_, prefix, err := net.ParseCIDR(fmt.Sprintf("2001:db8:%x00::/56", index+1))
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Dst: prefix, Protocol: unix.RTPROT_DHCP, Priority: 23}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	second, err := netlink.LinkByName("enmbrains0")
	if err != nil {
		t.Fatal(err)
	}
	localAddress, err := netlink.ParseAddr("fe80::10/64")
	if err != nil {
		t.Fatal(err)
	}
	localAddress.Flags = unix.IFA_F_NODAD
	localAddress.PreferedLft, localAddress.ValidLft = 0, 4294967295
	if err := netlink.AddrAdd(first, localAddress); err != nil {
		t.Fatal(err)
	}
	_, multipathPrefix, err := net.ParseCIDR("2001:db8:900::/64")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RouteAdd(&netlink.Route{Family: unix.AF_INET6, Dst: multipathPrefix, Table: 100, Protocol: unix.RTPROT_DHCP, Priority: 37, MultiPath: []*netlink.NexthopInfo{{LinkIndex: first.Attrs().Index, Gw: net.ParseIP("fe80::1"), Hops: 0}, {LinkIndex: second.Attrs().Index, Gw: net.ParseIP("fe80::2"), Hops: 2}}}); err != nil {
		t.Fatal(err)
	}
	rule := netlink.NewRule()
	rule.Family, rule.Table, rule.Priority, rule.Mark = unix.AF_INET, 100, 99, 1
	if err := netlink.RuleAdd(rule); err != nil {
		t.Fatal(err)
	}
	selectedRule := netlink.NewRule()
	selectedRule.Family, selectedRule.Table, selectedRule.Priority = unix.AF_INET, 200, 98
	selectedRule.IifName = "enatt0"
	selectedRule.UIDRange = netlink.NewRuleUIDRange(1001, 1003)
	_, selectedRule.Src, err = net.ParseCIDR("198.51.100.1/32")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.RuleAdd(selectedRule); err != nil {
		t.Fatal(err)
	}
	nft, err := nftables.New()
	if err != nil {
		t.Fatal(err)
	}
	table := nft.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv6, Name: "nat"})
	for _, name := range []string{"prerouting", "postrouting"} {
		chain := nft.AddChain(&nftables.Chain{Table: table, Name: name})
		nft.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{&expr.Counter{}, &expr.Verdict{Kind: expr.VerdictReturn}}})
	}
	if err := nft.Flush(); err != nil {
		t.Fatalf("install local nft rules: %v", err)
	}
	for _, view := range []string{"npt", "prefixes", "routes", "policy", "status", "stats", "sim4", "sim6"} {
		t.Run(view, func(t *testing.T) {
			output, diagnostics, code := runCLIProcess(t, binary, "debug", view, "--config", configPath, "--output-format", "json")
			if code != 0 {
				t.Fatalf("%s exit=%d stdout=%s stderr=%s", view, code, output, diagnostics)
			}
			if !json.Valid([]byte(output)) || strings.Count(output, "\n") != 1 {
				t.Fatalf("%s did not encode one record: %s", view, output)
			}
			started := time.Now()
			text, diagnostics, code := runCLIProcess(t, binary, "debug", view, "--config", configPath, "--output-format", "text")
			finished := time.Now()
			if code != 0 {
				t.Fatalf("text %s exit=%d stdout=%s stderr=%s", view, code, text, diagnostics)
			}
			if view != "npt" && text == "" {
				t.Fatalf("%s omitted its text result", view)
			}
			checkDebugInspectionJSON(t, view, output, text, started, finished)
			if view == "prefixes" || view == "status" || view == "stats" || view == "policy" || view == "sim4" || view == "sim6" {
				assertDebugInspectionFields(t, text, "  ID: \"att-line\"\n  name: \"att\"\n  interface: \"enatt0\"\n")
			}
		})
	}
	checkDebugSimulationPresent(t, binary, configPath, first)
	for _, format := range []string{"text", "json"} {
		full, err := os.OpenFile("/dev/full", os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(t.Context(), binary, "debug", "policy", "--config", configPath, "--output-format", format)
		command.Stdout = full
		var diagnostics bytes.Buffer
		command.Stderr = &diagnostics
		err = command.Run()
		if closeErr := full.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err == nil || !strings.Contains(diagnostics.String(), "no space left on device") {
			t.Fatalf("%s write error=%v stderr=%s", format, err, diagnostics.String())
		}
	}
	link, err := netlink.LinkByName("enatt0")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkDel(link); err != nil {
		t.Fatal(err)
	}
	output, diagnostics, code := runCLIProcess(t, binary, "debug", "status", "--config", configPath, "--output-format", "json")
	if code != 1 || output != "" || !strings.Contains(diagnostics, "enatt0: link state:") {
		t.Fatalf("failed collection exit=%d stdout=%s stderr=%s", code, output, diagnostics)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &document); err != nil {
		t.Fatal(err)
	}
	var interfaces map[string]json.RawMessage
	if err := json.Unmarshal(document["ietf-interfaces:interfaces"], &interfaces); err != nil {
		t.Fatal(err)
	}
	delete(interfaces, "goodkind-mwan-steering:steering-group")
	document["ietf-interfaces:interfaces"], err = json.Marshal(interfaces)
	if err != nil {
		t.Fatal(err)
	}
	missingSources, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(networkDir, "network.json"), missingSources, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, view := range []string{"sim4", "sim6"} {
		text, diagnostics, code := runCLIProcess(t, binary, "debug", view, "--config", configPath, "--output-format", "text")
		if code != 0 || !strings.Contains(diagnostics, "is unset; skipping probes") {
			t.Fatalf("absent source exit=%d stdout=%s stderr=%s", code, text, diagnostics)
		}
		assertDebugInspectionFields(t, text, "source: \"\"\nsource present: false\nlookups: 0\n")
	}
}

func checkDebugInspectionJSON(t *testing.T, view, output, text string, started, finished time.Time) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	switch view {
	case "npt":
		var actual npt.RenderedTable
		if err := json.Unmarshal([]byte(output), &actual); err != nil {
			t.Fatal(err)
		}
		expected, err := npt.RenderTable(context.Background(), logger)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual.Prerouting, expected.Prerouting) || !reflect.DeepEqual(actual.Postrouting, expected.Postrouting) || len(actual.Prerouting) != 1 || len(actual.Postrouting) != 1 {
			t.Fatalf("NPT rules changed: got=%+v want=%+v", actual, expected)
		}
	case "prefixes":
		var result struct {
			Prefixes []struct {
				Prefix  string
				Present bool
			}
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Prefixes) != 3 || !result.Prefixes[0].Present || result.Prefixes[0].Prefix != "2001:db8:100::/56" {
			t.Fatalf("prefix result=%+v", result)
		}
		assertDebugInspectionFields(t, text, "  present: true\n  prefix: \"2001:db8:100::/56\"\n")
	case "routes":
		var result struct {
			Family string
			Routes []netif.CurrentRoute
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		expected, err := netif.ListDHCPRoutes(context.Background(), logger, "inet6")
		if err != nil {
			t.Fatal(err)
		}
		if result.Family != "inet6" || !reflect.DeepEqual(result.Routes, expected) {
			t.Fatalf("route records changed: got=%+v want=%+v", result, expected)
		}
		assertDebugInspectionRoutes(t, text, result.Routes)
	case "policy":
		var result struct {
			IPv4   []netif.CurrentRule
			Tables []struct {
				WAN struct {
					ID      string
					Name    string
					TableID int `json:"table_id"`
				}
				IPv4 []netif.CurrentRoute
				IPv6 []netif.CurrentRoute
			}
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, value := range result.IPv4 {
			if value.Priority == 99 && value.Mark == 1 && value.TableID == 100 {
				found = true
			}
		}
		if !found || len(result.Tables) != 3 || result.Tables[0].WAN.Name != "att" || result.Tables[0].WAN.TableID != 100 {
			t.Fatalf("policy result=%+v", result)
		}
		for _, rule := range result.IPv4 {
			assertDebugInspectionFields(t, text, fmt.Sprintf("  priority: %d\n  from: %q\n  mark: %d\n  input interface: %q\n  UID range: %q\n  table ID: %d\n", rule.Priority, rule.From, rule.Mark, rule.IifName, rule.UIDRange, rule.TableID))
		}
		for _, table := range result.Tables {
			assertDebugInspectionRoutes(t, text, table.IPv4)
			assertDebugInspectionRoutes(t, text, table.IPv6)
		}
	case "status":
		var result struct {
			Interfaces []struct {
				State         netif.LinkState
				Addresses     []netif.CurrentAddr
				PrefixPresent bool `json:"prefix_present"`
			}
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Interfaces) != 3 || !result.Interfaces[0].PrefixPresent || len(result.Interfaces[0].Addresses) < 2 || !result.Interfaces[0].State.Carrier {
			t.Fatalf("status result=%+v", result)
		}
		for _, record := range result.Interfaces {
			assertDebugInspectionFields(t, text, fmt.Sprintf("oper: %q\ncarrier: %t\n", record.State.OperState, record.State.Carrier))
			for _, address := range record.Addresses {
				assertDebugInspectionFields(t, text, fmt.Sprintf("  CIDR: %q\n  family: %q\n  flags: %d\n  scope: %d\n  preferred lifetime (s): %d\n  valid lifetime (s): %d\n", address.CIDR, address.Family, address.Flags, address.Scope, address.PreferredLifetime, address.ValidLifetime), fmt.Sprintf("  origin: %q\n", address.Origin))
			}
		}
		assertDebugInspectionFields(t, text, "  CIDR: \"fe80::10/64\"\n", "  origin: \"unknown\"\n", "carrier: false\n")
		observations := 0
		for _, line := range strings.Split(text, "\n") {
			value, ok := strings.CutPrefix(line, "  observed at: ")
			if !ok {
				continue
			}
			observed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil || observed.Before(started) || observed.After(finished) || observed.Nanosecond()%1000000 == 0 {
				t.Fatalf("address timestamp %q does not preserve its observation: %v", value, err)
			}
			observations++
		}
		if observations < 7 {
			t.Fatalf("status omitted observation timestamps: %s", text)
		}
	case "stats":
		var result struct {
			Interfaces []struct {
				WAN   struct{ Interface string }
				Stats netif.LinkStats
			}
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Interfaces) != 3 {
			t.Fatalf("stats result=%+v", result)
		}
		for _, record := range result.Interfaces {
			expected, err := netif.ReadLinkStats(logger, record.WAN.Interface)
			if err != nil {
				t.Fatal(err)
			}
			if record.Stats != expected {
				t.Fatalf("counters changed: got=%+v want=%+v", record.Stats, expected)
			}
			stats := record.Stats
			assertDebugInspectionFields(t, text, fmt.Sprintf("RX: bytes %d packets %d errors %d dropped %d\nTX: bytes %d packets %d errors %d dropped %d\n", stats.RxBytes, stats.RxPackets, stats.RxErrors, stats.RxDropped, stats.TxBytes, stats.TxPackets, stats.TxErrors, stats.TxDropped))
		}
	case "sim4", "sim6":
		var result struct {
			Family        string
			Target        string
			Source        string
			SourcePresent bool `json:"source_present"`
			Lookups       []struct {
				Mark    uint32
				Present bool
				Route   netif.RouteLookupResult
			}
		}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			t.Fatal(err)
		}
		if !result.SourcePresent || len(result.Lookups) != 4 || result.Lookups[0].Mark != 0 {
			t.Fatalf("simulation result=%+v", result)
		}
		for _, lookup := range result.Lookups {
			if lookup.Present || lookup.Route != (netif.RouteLookupResult{}) {
				t.Fatalf("absent route changed: %+v", lookup)
			}
		}
		assertDebugInspectionFields(t, text, fmt.Sprintf("family: %q\ntarget: %q\nsource: %q\nsource present: true\n", result.Family, result.Target, result.Source), "default lookup:\n  mark: 0\n  present: false\n")
		if strings.Count(text, "  present: false\n  gateway: \"\"\n  output interface: \"\"\n  source: \"\"\n") != 4 {
			t.Fatalf("simulation omitted absent route values: %s", text)
		}
	}
}

func assertDebugInspectionFields(t *testing.T, text string, fields ...string) {
	t.Helper()
	for _, field := range fields {
		if !strings.Contains(text, field) {
			t.Fatalf("text omitted source fields %q: %s", field, text)
		}
	}
}

func assertDebugInspectionRoutes(t *testing.T, text string, routes []netif.CurrentRoute) {
	t.Helper()
	for _, route := range routes {
		assertDebugInspectionFields(t, text, fmt.Sprintf("  family: %q\n  destination: %q\n  gateway: %q\n  device: %q\n  table ID: %d\n  protocol: %d\n  metric: %d\n  scope: %d\n  type: %d\n  next hops: %d\n", route.Family, route.Dest, route.Via, route.Dev, route.TableID, route.Protocol, route.Metric, route.Scope, route.Type, len(route.NextHops)))
		for _, hop := range route.NextHops {
			assertDebugInspectionFields(t, text, fmt.Sprintf("    gateway: %q\n    device: %q\n    link index: %d\n    weight: %d\n", hop.Via, hop.Dev, hop.LinkIndex, hop.Weight))
		}
	}
}

func checkDebugSimulationPresent(t *testing.T, binary, config string, outputLink netlink.Link) {
	t.Helper()
	lan, err := netlink.LinkByName("enmwanbr0")
	if err != nil {
		t.Fatal(err)
	}
	for _, cidr := range []string{"192.0.2.2/29", "2001:db8:b01:fe::2/64"} {
		address, err := netlink.ParseAddr(cidr)
		if err != nil {
			t.Fatal(err)
		}
		address.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(lan, address); err != nil {
			t.Fatal(err)
		}
	}
	for _, cidr := range []string{"0.0.0.0/0", "::/0"} {
		_, destination, err := net.ParseCIDR(cidr)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.RouteAdd(&netlink.Route{Dst: destination, LinkIndex: outputLink.Attrs().Index}); err != nil {
			t.Fatal(err)
		}
	}
	for _, view := range []string{"sim4", "sim6"} {
		text, diagnostics, code := runCLIProcess(t, binary, "debug", view, "--config", config, "--output-format", "text")
		if code != 0 || diagnostics != "" {
			t.Fatalf("present simulation exit=%d stdout=%s stderr=%s", code, text, diagnostics)
		}
		if strings.Count(text, "  present: true\n  gateway: \"\"\n  output interface: \"enatt0\"\n") != 4 {
			t.Fatalf("simulation omitted present route values: %s", text)
		}
	}
}
