package provider_test

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

const networkRouteSchemaVersion = 0

type networkRouteState struct {
	Interfaces       map[string]networkInterface       `json:"interfaces"`
	Routes           map[string]networkRoute           `json:"routes"`
	ProviderDefaults map[string]networkProviderDefault `json:"provider_defaults"`
}

func networkRuleMapNames() []string {
	return []string{"policy_rules", "firewall_chains", "firewall_rules", "firewall_sets"}
}

func TestNetworkConfigUpgradesRouteState(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	ctx := context.Background()
	current := server.mustReadNetwork(t, readNetworkDocument(t, networkManagementEntry, networkManagementGateways))
	recorded, err := json.Marshal(networkRouteState{
		Interfaces:       decodeNetworkInterfaces(t, current),
		Routes:           decodeNetworkRoutes(t, current),
		ProviderDefaults: decodeNetworkProviderDefaults(t, current),
	})
	if err != nil {
		t.Fatalf("encode the version 0 state: %v", err)
	}

	response, err := server.server.UpgradeResourceState(ctx, &tfprotov6.UpgradeResourceStateRequest{
		TypeName: networkConfigResource,
		Version:  networkRouteSchemaVersion,
		RawState: &tfprotov6.RawState{JSON: recorded},
	})
	if err != nil {
		t.Fatalf("upgrade %s: %v", networkConfigResource, err)
	}
	failOnDiagnostics(t, "upgrade "+networkConfigResource, response.Diagnostics)
	upgraded := server.value(t, response.UpgradedState)

	for _, name := range []string{"interfaces", "routes", "provider_defaults"} {
		if got, want := attribute(t, upgraded, name), attribute(t, current, name); !got.Equal(want) {
			t.Errorf("upgraded %s:\ngot  %s\nwant %s", name, got, want)
		}
	}
	wantRouteKeys := []string{
		"enmgmt0|ipv4|0.0.0.0/0",
		"enmgmt0|ipv6|::/0",
		"enwebpass0|ipv4|198.18.0.0/24",
		"enwebpass0|ipv4|198.18.1.0/24",
		"enwebpass0|ipv4|198.18.2.0/24",
		"enwebpass0|ipv6|2001:db8:a00::/48",
		"enwebpass0|ipv6|2001:db8:a01::/48",
		"enwebpass0|ipv6|2001:db8:a02::/48",
	}
	if got := slices.Sorted(maps.Keys(objectMap(t, upgraded, "routes"))); !slices.Equal(got, wantRouteKeys) {
		t.Errorf("upgraded route keys = %v, want %v", got, wantRouteKeys)
	}
	for _, name := range networkRuleMapNames() {
		found := attribute(t, upgraded, name)
		if found.IsNull() || !found.IsKnown() {
			t.Errorf("upgraded %s = %s, want a known empty map", name, found)
			continue
		}
		if entries := objectMap(t, upgraded, name); len(entries) != 0 {
			t.Errorf("upgraded %s has %d entries, want none", name, len(entries))
		}
	}

	configured := server.networkConfig(t, current)
	routeOnly := configured
	for _, name := range networkRuleMapNames() {
		omitted := tftypes.NewValue(attribute(t, configured, name).Type(), nil)
		routeOnly = replaceAttribute(t, routeOnly, name, omitted)
	}
	planResponse, err := server.server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         networkConfigResource,
		PriorState:       server.dynamic(t, upgraded),
		ProposedNewState: server.dynamic(t, upgraded),
		Config:           server.dynamic(t, routeOnly),
	})
	if err != nil {
		t.Fatalf("plan the version 0 configuration: %v", err)
	}
	failOnDiagnostics(t, "plan the version 0 configuration", planResponse.Diagnostics)
	if len(planResponse.RequiresReplace) > 0 {
		t.Errorf("the version 0 configuration requires replacement for %v", planResponse.RequiresReplace)
	}
	if planned := server.value(t, planResponse.PlannedState); !planned.Equal(upgraded) {
		t.Errorf("the version 0 configuration changes the upgraded state:\ngot  %s\nwant %s", planned, upgraded)
	}

	state := server.planAndApply(t, upgraded, configured)
	if got, want := attribute(t, state, "routes"), attribute(t, upgraded, "routes"); !got.Equal(want) {
		t.Errorf("routes changed when the rule maps were added:\ngot  %s\nwant %s", got, want)
	}
	if got := len(objectMap(t, state, "firewall_rules")); got == 0 {
		t.Error("firewall_rules is empty after the configured rule maps were applied")
	}
}
