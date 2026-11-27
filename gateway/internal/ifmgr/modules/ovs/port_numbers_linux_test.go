//go:build linux && cgo

package ovs

// OpenFlow port numbers belong to a bridge. Two OVSDB bridge fixtures each
// assign port 2 to a different interface. The lookup for the first bridge
// returns only lan mapped to 2. The second returns only wan mapped to 2.

import (
	"testing"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

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
