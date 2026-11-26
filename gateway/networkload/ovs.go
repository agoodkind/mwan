package networkload

import (
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

var errOVSSelection = errors.New("set dataplane to ovs")

// LoadOVS loads and validates the supplied network configuration and requires OVS settings.
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
