//go:build cgo

package networkjson_test

import (
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/firewall"
	"goodkind.io/mwan/gateway/networkload"
)

const managementGatewayInstance = "../yang/instances/network-min.json"

func TestLoadFirewallAllowsSSHOnManagementInterface(t *testing.T) {
	t.Parallel()
	loaded, err := networkload.Load(managementGatewayInstance, schemaDirForTest(t))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	ruleset, err := firewall.Compile(loaded.Firewall)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	for _, chain := range ruleset.Chains {
		if chain.Name != "input" {
			continue
		}

		for _, rule := range chain.Rules {
			if strings.Contains(rule.Expression+" ", "dport 22 ") &&
				strings.Contains(rule.Expression, "enmgmt0") {
				return
			}
		}
	}

	t.Fatal("input chain has no SSH accept on the management interface")
}
