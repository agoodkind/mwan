package ovs

import (
	"errors"
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestCompileFlowsRejectsDuplicateSwitchMatches(t *testing.T) {
	var first, second ovsconfig.Flow
	first.Name = "discard"
	first.Priority = 100
	first.Match = []ovsconfig.Match{
		{Field: "eth_type", Value: "0x0800"},
		{Field: "in_port", Value: "1"},
	}
	second.Name = "forward"
	second.Priority = first.Priority
	second.Match = []ovsconfig.Match{
		{Field: "in_port", Value: "lan"},
		{Field: "ip"},
	}
	second.Actions = []ovsconfig.Action{{Name: "output", Argument: "wan"}}

	ports := map[string]uint32{"lan": 1, "wan": 2}
	_, err := compileFlows(t.Context(), []ovsconfig.Flow{first, second}, ports)
	if !errors.Is(err, errDuplicateFlow) || !strings.Contains(err.Error(), second.Name) {
		t.Fatalf("expected duplicate rule %q, got %v", second.Name, err)
	}

	second.Priority++
	if _, err := compileFlows(
		t.Context(),
		[]ovsconfig.Flow{first, second},
		ports,
	); err != nil {
		t.Fatalf("rules with different priorities: %v", err)
	}
}

func TestCompileFlowsRejectsCollidingRuleCookies(t *testing.T) {
	rules := []ovsconfig.Flow{
		{Name: "wan-1f12547414d7f8ec", Priority: 10},
		{Name: "wan-4796b608b453eb1a", Priority: 20},
	}
	flows, err := compileFlows(t.Context(), rules, nil)
	if err == nil || len(flows) != 0 || !strings.Contains(err.Error(), "cookie conflict") {
		t.Fatalf("compile rules with colliding cookies: flows=%v, err=%v", flows, err)
	}
}
