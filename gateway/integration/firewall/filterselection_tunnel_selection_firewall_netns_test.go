//go:build linux && firewall

package firewall_test

import (
	"testing"

	"goodkind.io/mwan/gateway/integration/internal/testenv"
)

func TestTunnelRuntimeIPv4SelectionRejectsAMissingAssignment(t *testing.T) {
	if selection, err := testenv.TunnelRuntimeIPv4Selection(); err == nil {
		t.Fatalf("a failed chain read returned selection %q without an error", selection)
	}
	ipv6Only := "\t\tip6 saddr 2001:db8:b01::/60 ct state new meta mark set 0x00000002\n"
	if selection := testenv.TunnelRuntimeIPv4Assignments(ipv6Only); selection != "" {
		t.Fatalf("a chain without an IPv4 assignment returned selection %q", selection)
	}
	balanced := "\t\tiifname \"enmwanbr0\" ip saddr 192.0.2.0/29 ct state new meta mark set jhash ip saddr mod 2 map { 0 : 0x1, 1 : 0x3 }\n"
	if selection := testenv.TunnelRuntimeIPv4Assignments(
		ipv6Only + balanced,
	); selection != "jhash ip saddr mod 2 map { 0 : 0x1, 1 : 0x3 }" {
		t.Fatalf("selection of a chain with one IPv4 assignment = %q", selection)
	}
}
