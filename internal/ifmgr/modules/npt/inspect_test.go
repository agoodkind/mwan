package npt

import (
	"net/netip"
	"testing"
)

func TestRenderedTableHasInterface(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		table RenderedTable
		iface string
		want  bool
	}{
		{
			name: "prerouting exact match",
			table: RenderedTable{
				Prerouting: []string{
					`iif "enatt0" ip6 daddr 2001:db8::/64 dnat prefix to fd00::/64`,
				},
			},
			iface: "enatt0",
			want:  true,
		},
		{
			name: "postrouting exact match",
			table: RenderedTable{
				Postrouting: []string{
					`oif "enatt0" ip6 saddr fd00::/64 snat prefix to 2001:db8::/64`,
				},
			},
			iface: "enatt0",
			want:  true,
		},
		{
			name: "absent interface",
			table: RenderedTable{
				Prerouting: []string{
					`iif "enatt0" ip6 daddr 2001:db8::/64 dnat prefix to fd00::/64`,
				},
			},
			iface: "enwebpass0",
			want:  false,
		},
		{
			name: "shorter substring collision",
			table: RenderedTable{
				Prerouting: []string{
					`iif "enatt0.3242" ip6 daddr 2001:db8::/64 dnat prefix to fd00::/64`,
				},
			},
			iface: "enatt0",
			want:  false,
		},
		{
			name: "longer substring collision",
			table: RenderedTable{
				Postrouting: []string{
					`oif "enatt0" ip6 saddr fd00::/64 snat prefix to 2001:db8::/64`,
				},
			},
			iface: "enatt0.3242",
			want:  false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := testCase.table.HasInterface(testCase.iface)
			if got != testCase.want {
				t.Fatalf(
					"HasInterface(%q) = %v, want %v",
					testCase.iface,
					got,
					testCase.want,
				)
			}
		})
	}
}

// TestRenderIntended pins the text form of the intended ruleset the
// management surface serves: one heading per chain, rules in program
// order, in the same wording the inspector renders live rules.
func TestRenderIntended(t *testing.T) {
	t.Parallel()
	desired := desiredRules{
		Prerouting: []natRule{{
			Chain: chainPrerouting, Iface: "enatt0",
			Match: netip.MustParsePrefix("2001:db8:a::/60"),
			Op:    opDNATPrefix, ToPfx: netip.MustParsePrefix("3d06:bad:b01::/60"),
		}},
		Postrouting: []natRule{{
			Chain: chainPostrouting, Iface: "enatt0",
			Match: netip.MustParsePrefix("3d06:bad:b01::/60"),
			Op:    opSNATPrefix, ToPfx: netip.MustParsePrefix("2001:db8:a::/60"),
		}},
	}
	want := "chain prerouting:\n" +
		"  iif \"enatt0\" ip6 daddr 2001:db8:a::/60 dnat prefix to 3d06:bad:b01::/60\n" +
		"chain postrouting:\n" +
		"  oif \"enatt0\" ip6 saddr 3d06:bad:b01::/60 snat prefix to 2001:db8:a::/60\n"
	if got := renderIntended(desired); got != want {
		t.Fatalf("renderIntended =\n%s\nwant:\n%s", got, want)
	}
}
