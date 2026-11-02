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
		link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: name}}
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
	rule := netlink.NewRule()
	rule.Family, rule.Table, rule.Priority, rule.Mark = unix.AF_INET, 100, 99, 1
	if err := netlink.RuleAdd(rule); err != nil {
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
			checkDebugInspectionJSON(t, view, output)
			text, diagnostics, code := runCLIProcess(t, binary, "debug", view, "--config", configPath, "--output-format", "text")
			if code != 0 {
				t.Fatalf("text %s exit=%d stdout=%s stderr=%s", view, code, text, diagnostics)
			}
			if view != "npt" && text == "" {
				t.Fatalf("%s omitted its text result", view)
			}
			if view == "sim4" || view == "sim6" {
				if strings.Count(text, ": no route") != 4 {
					t.Fatalf("%s did not preserve absent routes: %s", view, text)
				}
			}
		})
	}
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
}

func checkDebugInspectionJSON(t *testing.T, view, output string) {
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
		}
	case "sim4", "sim6":
		var result struct {
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
	}
}
