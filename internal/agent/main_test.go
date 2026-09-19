package agent

import (
	"reflect"
	"testing"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
)

func TestTablesFromConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		want []int
	}{
		{
			name: "main table without WAN sections",
			cfg:  &config.Config{},
			want: []int{unix.RT_TABLE_MAIN},
		},
		{
			name: "main and configured WAN tables",
			cfg: &config.Config{IfMgr: config.IfMgrSection{WAN: map[string]config.IfMgrWANEntry{
				"webpass": {TableID: 200},
				"att":     {TableID: 100},
			}}},
			want: []int{unix.RT_TABLE_MAIN, 100, 200},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := tablesFromConfig(test.cfg)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("tablesFromConfig() = %v, want %v", got, test.want)
			}
		})
	}
}
