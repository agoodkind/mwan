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
	_, err := compileFlows(t.Context(), []ovsconfig.Flow{first, second}, ports)
	if !errors.Is(err, errDuplicateFlow) || !strings.Contains(err.Error(), second.Name) {
		t.Fatalf("expected duplicate rule %q, got %v", second.Name, err)
	}

	second.Priority++
	if _, err := compileFlows(t.Context(), []ovsconfig.Flow{first, second}, ports); err != nil {
		t.Fatalf("rules with different priorities: %v", err)
	}
}
