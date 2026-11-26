//go:build linux && cgo

package ovs

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestReadableFlowWire(t *testing.T) {
	var readable, original ovsconfig.Flow
	port := "lan"
	ipv4 := uint16(0x0800)
	original.Priority = 100
	original.InPort = port
	original.EtherType = &ipv4

	for _, instruction := range []string{"AAQAGAAAAAAAGQAQgAAWBMsAcS0AAAAA", "AAEACAEAAAA="} {
		raw, err := base64.StdEncoding.DecodeString(instruction)
		if err != nil {
			t.Fatal(err)
		}

		original.RawInstruction = append(original.RawInstruction, raw)
	}

	readable.Priority = original.Priority
	readable.Match = []ovsconfig.Match{{Field: "in_port", Value: port}, {Field: "ip"}}
	readable.Actions = []ovsconfig.Action{
		{ID: 1, Name: "set_field", Argument: "203.0.113.45->ip_src"},
		{ID: 2, Name: "goto_table", Argument: "1"},
	}
	wire := func(rule ovsconfig.Flow) []byte {
		t.Helper()
		flow, err := marshalFlow(t.Context(), rule, map[string]uint32{port: 2})
		if err != nil {
			t.Fatal(err)
		}

		flow.Xid = 1
		data, err := flow.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}

		return data
	}
	want := wire(original)
	if got := wire(readable); !bytes.Equal(got, want) {
		t.Fatalf("readable flow %x differs from original %x", got, want)
	}
}

func TestRejectInvalidExpressions(t *testing.T) {
	var invalid ovsconfig.Flow
	invalid.Name = "rewrite-without-ip"
	invalid.Actions = []ovsconfig.Action{
		{ID: 1, Name: "set_field", Argument: "203.0.113.45->ip_src"},
	}
	if _, err := compileFlows(t.Context(), []ovsconfig.Flow{invalid}, nil, nil, nil); err == nil ||
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
