package networkload

// OVS callers use this loader when they require a WANCONFIG document that
// selects OVS. It shares schema and semantic validation with the daemon.

import (
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

var errOVSSelection = errors.New("set dataplane to ovs")

// LoadOVS reads OVS settings from a WAN network document.
//
// LoadOVS validates the document against the supplied YANG schema. A valid
// document that selects kernel forwarding still returns an error.
//
// A bridge inventory can print the validated bridge name and port count:
//
//	settings, err := networkload.LoadOVS(networkPath, schemaDir)
//	if err != nil {
//		return err
//	}
//	fmt.Printf("%s: %d configured ports\n", settings.Bridge, len(settings.Ports))
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
