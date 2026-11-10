package steering

import (
	"testing"

	"github.com/google/nftables"
)

func TestNftEventWipesSteering(t *testing.T) {
	t.Parallel()

	steerTable := &nftables.Table{Family: nftables.TableFamilyINet, Name: steerTableName}
	otherInetTable := &nftables.Table{Family: nftables.TableFamilyINet, Name: "filter"}
	v6SteerTable := &nftables.Table{Family: nftables.TableFamilyIPv6, Name: steerTableName}

	tests := []struct {
		name  string
		event *nftables.MonitorEvent
		want  bool
	}{
		{
			name:  "delete the steering table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: steerTable},
			want:  true,
		},
		{
			name: "delete the steering chain",
			event: &nftables.MonitorEvent{
				Type: nftables.MonitorEventTypeDelChain,
				Data: &nftables.Chain{Name: steerChainName, Table: steerTable},
			},
			want: true,
		},
		{
			// The module's own Apply flushes the chain on every reconcile,
			// which emits rule deletes. Matching them would make each pass
			// request another and spin.
			name: "delete a steering rule is not a wipe",
			event: &nftables.MonitorEvent{
				Type: nftables.MonitorEventTypeDelRule,
				Data: &nftables.Rule{Table: steerTable, Chain: &nftables.Chain{Name: steerChainName}},
			},
			want: false,
		},
		{
			// Apply creates the table and the chain every pass, so their
			// creation events must not be a wipe signal either.
			name:  "create the steering table is not a wipe",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeNewTable, Data: steerTable},
			want:  false,
		},
		{
			name:  "delete a different inet table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: otherInetTable},
			want:  false,
		},
		{
			name:  "delete a same-named table in another family",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelTable, Data: v6SteerTable},
			want:  false,
		},
		{name: "nil event", event: nil, want: false},
		{
			name:  "delete a chain with no table",
			event: &nftables.MonitorEvent{Type: nftables.MonitorEventTypeDelChain, Data: &nftables.Chain{}},
			want:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := nftEventWipesSteering(test.event); got != test.want {
				t.Fatalf("nftEventWipesSteering = %t, want %t", got, test.want)
			}
		})
	}
}
