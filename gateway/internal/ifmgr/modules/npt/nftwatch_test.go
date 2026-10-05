package npt

import (
	"testing"

	"github.com/google/nftables"
)

func TestNftEventWipesNAT(t *testing.T) {
	t.Parallel()

	natTable := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: natTableName}
	otherV6Table := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: "filter"}
	v4NatTable := &nftables.Table{Family: nftables.TableFamilyIPv4, Name: natTableName}

	tests := []struct {
		name  string
		event *nftables.MonitorEvent
		want  bool
	}{
		{
			name:  "delete ip6 nat table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: natTable},
			want:  true,
		},
		{
			name: "delete ip6 nat chain",
			event: &nftables.MonitorEvent{
				Type: nftables.MonitorEventTypeDelChain,
				Data: &nftables.Chain{Name: postroutingChain, Table: natTable},
			},
			want: true,
		},
		{
			// A rule delete must NOT count as a wipe: npt's own Apply flushes
			// both chains (DelRule events) on every reconcile, so matching
			// DelRule would make each reconcile request another and spin.
			name: "delete ip6 nat rule is not a wipe (self-trigger guard)",
			event: &nftables.MonitorEvent{
				Type: nftables.MonitorEventTypeDelRule,
				Data: &nftables.Rule{Table: natTable, Chain: &nftables.Chain{Name: postroutingChain}},
			},
			want: false,
		},
		{
			name:  "create ip6 nat table is not a wipe",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeNewTable, Data: natTable},
			want:  false,
		},
		{
			name:  "delete a different ip6 table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: otherV6Table},
			want:  false,
		},
		{
			name:  "delete the ip4 nat table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: v4NatTable},
			want:  false,
		},
		{
			name:  "nil event",
			event: nil,
			want:  false,
		},
		{
			name:  "delete chain with nil table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelChain, Data: &nftables.Chain{}},
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := nftEventWipesNAT(test.event); got != test.want {
				t.Fatalf("nftEventWipesNAT = %t, want %t", got, test.want)
			}
		})
	}
}
