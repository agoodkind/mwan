//go:build linux && cgo

package ovs

// OpenFlow action order comes from the list. A Linux parser test encodes two
// rewrites of the same IPv4 source field in list order even though the first
// action has the larger ID. It also checks both group references inside clone.

import (
	"bytes"
	"testing"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

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
