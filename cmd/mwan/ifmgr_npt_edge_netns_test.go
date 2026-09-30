//go:build linux && firewallnetns

package main

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func assertRuntimeTranslationSettled(t *testing.T, daemon *runtimeDaemon) {
	t.Helper()
	const reason = "NPT translation readiness changed"
	before := strings.Count(runtimeDaemonLog(t, daemon), reason)
	time.Sleep(500 * time.Millisecond)
	assertRuntimeDaemonRunning(t, daemon)
	if after := strings.Count(runtimeDaemonLog(t, daemon), reason); after != before {
		t.Fatalf("stable NPT state requested another reconcile: before=%d after=%d", before, after)
	}
}

func checkRuntimeLegacyFirstStart(t *testing.T, configPath, root string) {
	t.Helper()
	legacyBinary := os.Getenv("MWAN_NPT_LEGACY_BINARY")
	if legacyBinary == "" {
		return
	}
	config, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyConfig := strings.Split(string(config), "[ifmgr.modules.addresses]")[0]
	if err := os.WriteFile(configPath, []byte(legacyConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	old := startRuntimeDaemon(t, legacyBinary, configPath, root, "mapped-old-unjournaled")
	waitMappedRuntimeRule(t, old, "ip6", "nat", "2001:db8:beef:600::1", 10*time.Second)
	assertRuntimeDaemonRunning(t, old)
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	killOwnedRuntimeDaemon(t, old)
	if err := os.WriteFile(configPath, config, 0o600); err != nil {
		t.Fatal(err)
	}
	current := startRuntimeDaemon(t, os.Getenv(mappedRuntimeBinaryEnv), configPath, root, "mapped-first-journal-start")
	defer killOwnedRuntimeDaemon(t, current)
	waitStaticRuntimeLog(t, current, "address 2001:db8:beef:600::1/128 already exists without ownership record")
	waitStaticRuntimeAddress(t, current, "enwebpass0", "2001:db8:beef:600::1/128", true)
	assertRuntimeNPTEdges(t, filepath.Join(root, "owned-addresses.json"))
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("nft", "list", "table", "ip6", "nat").CombinedOutput()
		if err != nil {
			t.Fatalf("read retained first-start NPT rules: %v: %s", err, output)
		}
		if !strings.Contains(string(output), "2001:db8:beef:600::1") {
			assertRuntimeDaemonRunning(t, current)
			t.Log("same-boot first journal startup rejected the unrecorded edge, removed prior NPT rules, preserved the edge, and remained running")
			return
		}
		assertRuntimeDaemonRunning(t, current)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("first journal startup retained rejected edge translation")
}

func waitRuntimeNPTEdgeReady(t *testing.T, daemon *runtimeDaemon, iface, cidr string) {
	t.Helper()
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		link, err := netlink.LinkByName(iface)
		if err != nil {
			t.Fatal(err)
		}
		addresses, err := netlink.AddrList(link, unix.AF_INET6)
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range addresses {
			if address.IP.String() == prefix.Addr().String() && address.Flags&(unix.IFA_F_TENTATIVE|unix.IFA_F_DADFAILED) == 0 {
				return
			}
		}
		assertRuntimeDaemonRunning(t, daemon)
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("NPT edge %s did not become ready: %s", cidr, runtimeLogTail(t, daemon, 50))
}

func writeRuntimeLegacyNPT(t *testing.T, directory, prefix string, removeWANs bool) {
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
	var retained []map[string]json.RawMessage
	for _, entry := range entries {
		if string(entry["goodkind-mwan-steering:owner"]) == `"mwan"` {
			continue
		}
		delete(entry, "goodkind-mwan-steering:link")
		delete(entry, "ietf-ip:ipv4")
		delete(entry, "ietf-ip:ipv6")
		if string(entry["name"]) == `"enwebpass0"` {
			entry["goodkind-mwan-steering:owner"] = json.RawMessage(`"networkd"`)
			entry["goodkind-mwan-steering:link-files"] = json.RawMessage(`"hand-authored"`)
			delete(entry, "goodkind-mwan-steering:link")
			entry["ietf-ip:ipv6"] = json.RawMessage(fmt.Sprintf(`{"goodkind-mwan-steering:translation":{"mode":"ietf-nat:nptv6","nptv6":{"internal-prefix":"2001:db8:b01::/60","external-source":"configured","external-prefix":%q}}}`, prefix))
		}
		if removeWANs {
			delete(entry, "ietf-ip:ipv6")
		}
		retained = append(retained, entry)
	}
	interfaces["interface"], err = json.Marshal(retained)
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

func assertRuntimeNPTEdges(t *testing.T, path string, want ...string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var journal struct {
		Objects []struct {
			Scope  string `json:"scope"`
			Prefix string `json:"prefix"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(data, &journal); err != nil {
		t.Fatal(err)
	}
	var actual []string
	for _, record := range journal.Objects {
		if slices.Contains(want, record.Prefix) && record.Scope != "npt-edge" {
			t.Fatalf("edge %s was journaled as ordinary acquisition: %s", record.Prefix, data)
		}
		if record.Scope == "npt-edge" {
			actual = append(actual, record.Prefix)
		}
	}
	slices.Sort(actual)
	slices.Sort(want)
	if !slices.Equal(actual, want) {
		t.Fatalf("journaled NPT edges = %v; expected %v: %s", actual, want, data)
	}
}

func setMappedRuntimeIncompatibleChain(t *testing.T) {
	t.Helper()
	for _, arguments := range [][]string{
		{"delete", "chain", "ip6", "nat", "postrouting"},
		{"add", "chain", "ip6", "nat", "postrouting", "{", "type", "filter", "hook", "postrouting", "priority", "100", ";", "policy", "accept", ";", "}"},
	} {
		if output, err := exec.Command("nft", arguments...).CombinedOutput(); err != nil {
			t.Fatalf("replace NPT postrouting chain: %v: %s", err, output)
		}
	}
}
