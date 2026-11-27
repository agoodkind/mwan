package ovs

// A fake OVS database supplies the switch setting for one-way UDP connection
// tracking offload. The test checks that readSwitch rejects it when either the
// datapath or offload mode selects DOCA.

import (
	"context"
	"errors"
	"testing"

	"github.com/ovn-kubernetes/libovsdb/client"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

type readinessDatabase struct {
	client.Client
}

func (readinessDatabase) List(_ context.Context, result any) error {
	rows := result.(*[]switchRow)
	*rows = []switchRow{
		{
			DatapathTypes: []string{"netdev", "doca"},
			OtherConfig:   map[string]string{"hw-offload-ct-unidir-udp-enabled": "true"},
		},
	}

	return nil
}

func TestReadSwitchUnidirectionalUDP(t *testing.T) {
	db := databaseClient{Client: readinessDatabase{}}

	for _, test := range []struct {
		datapath string
		offload  string
		want     error
	}{
		{"doca", "", errUnidirectionalUDP},
		{"doca", "none", errUnidirectionalUDP},
		{"netdev", "doca", errUnidirectionalUDP},
		{"netdev", "", nil},
	} {
		var config ovsconfig.Config
		config.DatapathType = test.datapath
		config.Offload = test.offload
		_, err := readSwitch(t.Context(), db, config)
		if !errors.Is(err, test.want) {
			t.Errorf("%s/%s: got %v, want %v", test.datapath, test.offload, err, test.want)
		}
	}
}
