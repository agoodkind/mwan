//go:build cgo

package networkjson_test

import (
	"os"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/networkload"
)

const (
	lxcInstance          = "../../yang/instances/network-lxc.json"
	qemuInstance         = "../../yang/instances/network-min.json"
	lxcGuestTypeLeaf     = `"goodkind-mwan-steering:guest-type": "lxc",` + "\n"
	qemuGuestTypeLeaf    = `"goodkind-mwan-steering:guest-type": "qemu",` + "\n"
	managementRequiredIn = "management interface"
)

func instanceBody(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(body)
}

func replaceOnce(t *testing.T, body string, old string, replacement string) string {
	t.Helper()
	updated := strings.Replace(body, old, replacement, 1)
	if updated == body {
		t.Fatalf("document omits %q", old)
	}
	return updated
}

func TestLoadGuestTypeDefaultsToQEMUWhenAbsent(t *testing.T) {
	t.Parallel()
	loaded, err := networkload.Load(qemuInstance, schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, rule := range compiledInputRules(t, loaded.Firewall) {
		if strings.Contains(rule+" ", "dport 22 ") && strings.Contains(rule, "enmgmt0") {
			found = true
		}
	}
	if !found {
		t.Fatal("input chain has no SSH accept on the management interface")
	}
}

func TestLoadRejectsLXCManagementServicesWithoutInterface(t *testing.T) {
	t.Parallel()
	lxcBody := instanceBody(t, lxcInstance)
	withService := replaceOnce(t, lxcBody, `"firewall": {`,
		`"firewall": { "management-service": [{ "protocol": "tcp", "port": 22 }],`)
	_, err := networkload.Load(writeDocument(t, withService), schemaDirForTest(t))
	if err == nil {
		t.Fatal("Load accepted an lxc document with a management service and no interface")
	}
	if !strings.Contains(err.Error(), "management services require a management interface") {
		t.Fatalf("error = %v, want the management interface requirement", err)
	}
	if _, err := networkjson.LoadBaseline(writeDocument(t, withService)); err == nil {
		t.Fatal("LoadBaseline accepted the same document")
	}
}

func compiledInputRules(t *testing.T, cfg firewall.Config) []string {
	t.Helper()
	ruleset, err := firewall.Compile(cfg)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	var inputRules []string
	for _, chain := range ruleset.Chains {
		if chain.Name != "input" {
			continue
		}
		for _, rule := range chain.Rules {
			inputRules = append(inputRules, rule.Expression)
		}
	}
	if len(inputRules) == 0 {
		t.Fatal("ruleset has no input chain")
	}
	return inputRules
}

func TestLoadGuestTypeLXCWithoutManagementInterface(t *testing.T) {
	t.Parallel()
	loaded, err := networkload.Load(lxcInstance, schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.GuestType != config.GuestTypeLXC {
		t.Fatalf("guest type = %q, want %q", loaded.GuestType, config.GuestTypeLXC)
	}
	if !loaded.Firewall.Enabled || loaded.Firewall.ManagementInterface != "" {
		t.Fatalf("firewall = %+v, want enabled with no management interface", loaded.Firewall)
	}
	if len(loaded.Firewall.ManagementServices) != 0 {
		t.Fatalf("management services = %+v, want none", loaded.Firewall.ManagementServices)
	}
}

func TestLoadRejectsMissingManagementInterfaceUnlessLXC(t *testing.T) {
	t.Parallel()
	lxcBody := instanceBody(t, lxcInstance)
	cases := map[string]string{
		"qemu":   replaceOnce(t, lxcBody, lxcGuestTypeLeaf, qemuGuestTypeLeaf),
		"absent": replaceOnce(t, lxcBody, lxcGuestTypeLeaf, ""),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := networkload.Load(writeDocument(t, body), schemaDirForTest(t))
			if err == nil {
				t.Fatal("Load accepted a document with no management interface")
			}
			if !strings.Contains(err.Error(), managementRequiredIn) {
				t.Fatalf("error = %v, want it to contain %q", err, managementRequiredIn)
			}
		})
	}
}

func TestLoadBaselineRequiresManagementInterfaceUnlessLXC(t *testing.T) {
	t.Parallel()
	lxcBody := instanceBody(t, lxcInstance)
	baseline, err := networkjson.LoadBaseline(writeDocument(t, lxcBody))
	if err != nil {
		t.Fatalf("LoadBaseline lxc: %v", err)
	}
	if baseline == nil || baseline.ManagementInterface != "" {
		t.Fatalf("baseline = %+v, want no management interface", baseline)
	}
	qemuBody := replaceOnce(t, lxcBody, lxcGuestTypeLeaf, qemuGuestTypeLeaf)
	if _, err := networkjson.LoadBaseline(writeDocument(t, qemuBody)); err == nil {
		t.Fatal("LoadBaseline accepted a qemu document with no management interface")
	}
}

func TestCompileLXCOpensNoManagementService(t *testing.T) {
	t.Parallel()
	loaded, err := networkload.Load(lxcInstance, schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	inputRules := compiledInputRules(t, loaded.Firewall)
	for _, port := range []string{"tcp dport 179 ", "udp dport 3784 ", "udp dport 3785 "} {
		found := false
		for _, rule := range inputRules {
			if strings.Contains(rule, `iifname "enmwanbr0"`) && strings.Contains(rule+" ", port) {
				found = true
			}
		}
		if !found {
			t.Fatalf("input chain has no %q accept on the internal interface", port)
		}
	}
	for _, rule := range inputRules {
		for _, port := range []string{"dport 22 ", "dport 50052 ", "dport 443 ", "dport 8443 "} {
			if strings.Contains(rule+" ", port) {
				t.Fatalf("input rule %q accepts a management port", rule)
			}
		}
		if strings.Contains(rule, "enmgmt0") {
			t.Fatalf("input rule %q matches the management interface", rule)
		}
	}
}
