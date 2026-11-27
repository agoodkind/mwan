//go:build linux && cgo

package ovs

// OpenFlow compilation uses the Linux OVS parser. These cases check that the
// compiler reports an IPv4 source rewrite as inconsistent without an IP match
// and rejects malformed expressions in match and action fields.

import (
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestRejectInvalidExpressions(t *testing.T) {
	var invalid ovsconfig.Flow
	invalid.Name = "rewrite-without-ip"
	invalid.Actions = []ovsconfig.Action{
		{ID: 1, Name: "set_field", Argument: "203.0.113.45->ip_src"},
	}
	if _, err := compileFlows(t.Context(), []ovsconfig.Flow{invalid}, nil); err == nil ||
		!strings.Contains(
			err.Error(),
			invalid.Name,
		) || !strings.Contains(err.Error(), "MATCH_INCONSISTENT") {
		t.Fatalf("expected named inconsistent match error, got %v", err)
	}

	for _, field := range []ovsconfig.Match{
		{Field: "ip", Value: "cookie=0"},
		{Field: "table", Value: "2"},
		{Field: "not_a_field", Value: "2"},
		{Field: "in_port", Value: "not-a-port"},
	} {
		if _, err := marshalMatchFields([]ovsconfig.Match{field}, nil); err == nil {
			t.Errorf("accepted match %+v", field)
		}
	}

	for _, action := range []ovsconfig.Action{
		{Name: "output", Argument: "1),output(2"},
		{Name: "drop,output", Argument: "1"},
		{Name: "group", Argument: "65536"},
		{Name: "unknown_action"},
	} {
		if _, err := marshalActionFields(
			instructionExpression,
			[]ovsconfig.Action{action},
			nil,
		); err == nil {
			t.Errorf("accepted action %+v", action)
		}
	}
}
