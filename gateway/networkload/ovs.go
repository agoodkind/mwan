package networkload

import (
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

var errOVSSelection = errors.New("set dataplane to ovs")

// LoadOVS validates network configuration and requires OVS forwarding.
//
// Each reconciliation reads bridge, port, and OpenFlow settings from disk.
//
//	settings, err := networkload.LoadOVS(networkPath, schemaDir)
func LoadOVS(path, schemaDir string) (ovsconfig.Config, error) {
	loaded, err := Load(path, schemaDir)
	if err != nil {
		return ovsconfig.Config{}, err
	}

	if loaded.OVS == nil {
		slog.Error("OVS configuration is missing", "path", path, "err", errOVSSelection)

		return ovsconfig.Config{}, fmt.Errorf("%w in %s", errOVSSelection, path)
	}

	return *loaded.OVS, nil
}
