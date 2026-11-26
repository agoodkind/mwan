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

func TestOrderedActionsAndNestedGroups(t *testing.T) {
	actions := []ovsconfig.Action{
		{ID: 99, Name: "set_field", Argument: "192.0.2.1->ip_src"},
		{ID: 1, Name: "set_field", Argument: "192.0.2.2->ip_src"},
		{ID: 2, Name: "clone", Argument: "group:17,group:18"},
	}
	wire, err := marshalActionFields(bucketExpression, actions, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(wire) != 3 || !bytes.Contains(wire[0], []byte{192, 0, 2, 1}) ||
		!bytes.Contains(wire[1], []byte{192, 0, 2, 2}) ||
		!bytes.Contains(wire[2], []byte{0x6d, 0x77, 0, 17}) ||
		!bytes.Contains(wire[2], []byte{0x6d, 0x77, 0, 18}) {
		t.Fatalf("unexpected ordered action bytes %x", wire)
	}
}

func TestBridgePortNumbers(t *testing.T) {
	number := 2
	ports := []*portRow{
		{UUID: "p1", Interfaces: []string{"i1"}},
		{UUID: "p2", Interfaces: []string{"i2"}},
	}
	interfaces := []*interfaceRow{
		{UUID: "i1", Name: "lan", OpenFlow: &number},
		{UUID: "i2", Name: "wan", OpenFlow: &number},
	}
	configured := []ovsconfig.Port{{Name: "lan"}, {Name: "wan"}}

	for index, name := range []string{"lan", "wan"} {
		bridge := &bridgeRow{Ports: []string{ports[index].UUID}}
		numbers := bridgePortNumbers(bridge, ports, interfaces, configured)
		if len(numbers) != 1 || numbers[name] != 2 {
			t.Fatalf("bridge with port %s resolved %v", name, numbers)
		}
	}
}
