package netif

import (
	"net"
	"testing"
)

func TestRulesMatch(t *testing.T) {
	cur := CurrentRule{Priority: 5, Mark: 0x42, IifName: "lan0", UIDRange: "997-997", TableID: 500}
	cases := []struct {
		name string
		want DesiredRule
		ok   bool
	}{
		{"exact", DesiredRule{Priority: 5, Mark: 0x42, IifName: "lan0", UIDRange: "997-997", TableID: 500}, true},
		{"diff prio", DesiredRule{Priority: 6, Mark: 0x42, IifName: "lan0", UIDRange: "997-997", TableID: 500}, false},
		{"diff mark", DesiredRule{Priority: 5, Mark: 0x43, IifName: "lan0", UIDRange: "997-997", TableID: 500}, false},
		{"diff iif", DesiredRule{Priority: 5, Mark: 0x42, IifName: "lan1", UIDRange: "997-997", TableID: 500}, false},
		{"diff uid", DesiredRule{Priority: 5, Mark: 0x42, IifName: "lan0", UIDRange: "996-996", TableID: 500}, false},
		{"diff table", DesiredRule{Priority: 5, Mark: 0x42, IifName: "lan0", UIDRange: "997-997", TableID: 254}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := rulesMatch(cur, tc.want)
			if got != tc.ok {
				t.Fatalf("rulesMatch(%+v, %+v) = %v, want %v",
					cur, tc.want, got, tc.ok)
			}
		})
	}
}

func TestRuleMarkAndIifRoundTrip(t *testing.T) {
	want := DesiredRule{
		Family:   "inet",
		Priority: 100,
		From:     "192.0.2.10",
		Mark:     0x42,
		IifName:  "lan0",
		UIDRange: "997-997",
		TableID:  500,
	}

	rule, err := buildNetlinkRule("inet", want)
	if err != nil {
		t.Fatalf("buildNetlinkRule: %v", err)
	}
	if rule.Mark != want.Mark {
		t.Fatalf("mark got %#x, want %#x", rule.Mark, want.Mark)
	}
	if rule.IifName != want.IifName {
		t.Fatalf("iif got %q, want %q", rule.IifName, want.IifName)
	}

	current := ruleToCurrent(*rule)
	if current.Mark != want.Mark {
		t.Fatalf("current mark got %#x, want %#x", current.Mark, want.Mark)
	}
	if current.IifName != want.IifName {
		t.Fatalf("current iif got %q, want %q", current.IifName, want.IifName)
	}
	if !rulesMatch(current, want) {
		t.Fatalf("rulesMatch should match round-tripped rule: current=%+v want=%+v", current, want)
	}
}

func TestParseUIDRangeUint32(t *testing.T) {
	cases := []struct {
		in      string
		wantLo  uint32
		wantHi  uint32
		wantErr bool
	}{
		{"997-997", 997, 997, false},
		{"100-200", 100, 200, false},
		{"42", 42, 42, false},
		{"abc", 0, 0, true},
		{"100-x", 0, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			lo, hi, err := parseUIDRangeUint32(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if !tc.wantErr && (lo != tc.wantLo || hi != tc.wantHi) {
				t.Fatalf("got %d-%d, want %d-%d", lo, hi, tc.wantLo, tc.wantHi)
			}
		})
	}
}

func TestParseSelectorIPv6Single(t *testing.T) {
	// RFC 3849 documentation prefix
	ipnet, err := parseSelector("inet6", "2001:db8::1")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ones, _ := ipnet.Mask.Size(); ones != 128 {
		t.Fatalf("expected /128 mask, got /%d", ones)
	}
}

func TestParseSelectorIPv4CIDR(t *testing.T) {
	// RFC 5737 documentation prefix
	ipnet, err := parseSelector("inet", "192.0.2.0/24")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if ones, _ := ipnet.Mask.Size(); ones != 24 {
		t.Fatalf("expected /24 mask, got /%d", ones)
	}
	if !ipnet.IP.Equal(net.ParseIP("192.0.2.0").To4()) {
		t.Fatalf("expected 192.0.2.0, got %s", ipnet.IP)
	}
}

func TestIsAllAddr(t *testing.T) {
	if !isAllAddr(nil) {
		t.Fatal("nil should be 'all'")
	}
	if !isAllAddr(&net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)}) {
		t.Fatal("0.0.0.0/0 should be 'all'")
	}
	if !isAllAddr(&net.IPNet{IP: net.IPv6unspecified, Mask: net.CIDRMask(0, 128)}) {
		t.Fatal("::/0 should be 'all'")
	}
	if isAllAddr(&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(32, 32)}) {
		t.Fatal("192.0.2.1/32 should not be 'all'")
	}
}
