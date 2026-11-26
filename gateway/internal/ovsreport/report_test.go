package ovsreport

import (
	"fmt"
	"testing"
)

func TestReportedForwarding(t *testing.T) {
	for _, test := range []struct{ total, offload, want string }{
		{"12", "0", "software"},
		{"12", "12", "hardware"},
		{"12", "1", "mixed"},
		{"0", "0", "no-traffic"},
		{"18446744073709551615", "0", "unavailable"},
		{"12", "18446744073709551615", "unavailable"},
	} {
		line := fmt.Sprintf(
			"duration=2s, n_packets=%s, n_bytes=800, n_offload_packets=%s, n_offload_bytes=0, ip,actions=output:2",
			test.total,
			test.offload,
		)
		rules, err := unmarshalRules(line)
		if err != nil || len(rules) != 1 || rules[0].Forwarding != test.want {
			t.Fatalf("parse(%q) = %v, %v, want %s", line, rules, err, test.want)
		}
	}

	// The BlueField M16 TCP run recorded total counters without per-rule offload counters.
	rules, err := unmarshalRules(
		"cookie=0x0, duration=2317.331s, table=0, n_packets=467913, " +
			"n_bytes=42710930, idle_age=1179, priority=300,tcp6,in_port=2," +
			"ipv6_src=fd12:3456:789a::/48,tp_src=0x1388/0xfffc actions=ct(table=1,zone=1)",
	)
	if err != nil || len(rules) != 1 || rules[0].Forwarding != "unavailable" {
		t.Fatalf("missing offload counter: %v, %v", rules, err)
	}
}
