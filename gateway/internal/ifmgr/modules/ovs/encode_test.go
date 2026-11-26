package ovs

import (
	"errors"
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestCompileFlowsRejectsDuplicateSwitchMatches(t *testing.T) {
	ipv4 := uint16(0x0800)
	var first, second ovsconfig.Flow
	first.Name = "discard"
	first.Priority = 100
	first.RawMatch = [][]byte{
		{0x80, 0, 0x0a, 2, 0x08, 0},
		{0x80, 0, 0, 4, 0, 0, 0, 1},
	}
	second.Name = "forward"
	second.Priority = first.Priority
	second.InPort = "lan"
	second.EtherType = &ipv4
	second.Output = []string{"wan"}

	ports := map[string]uint32{"lan": 1, "wan": 2}
	_, err := compileFlows(t.Context(), []ovsconfig.Flow{first, second}, ports, nil, nil)
	if !errors.Is(err, errDuplicateFlow) || !strings.Contains(err.Error(), second.Name) {
		t.Fatalf("expected duplicate rule %q, got %v", second.Name, err)
	}

	second.Priority++
	if _, err := compileFlows(
		t.Context(),
		[]ovsconfig.Flow{first, second},
		ports,
		nil,
		nil,
	); err != nil {
		t.Fatalf("rules with different priorities: %v", err)
	}
}

func TestCompileFlowsRejectsCollidingRuleCookies(t *testing.T) {
	rules := []ovsconfig.Flow{
		{Name: "wan-1f12547414d7f8ec", Priority: 10},
		{Name: "wan-4796b608b453eb1a", Priority: 20},
	}
	flows, err := compileFlows(t.Context(), rules, nil, nil, nil)
	if err == nil || len(flows) != 0 || !strings.Contains(err.Error(), "cookie conflict") {
		t.Fatalf("compile rules with colliding cookies: flows=%v, err=%v", flows, err)
	}
}
