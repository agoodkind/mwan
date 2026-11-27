//go:build linux && cgo

package ovs

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
