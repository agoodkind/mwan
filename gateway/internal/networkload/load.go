// Package networkload separates native network loading from networkjson
// because the provider's cgo-disabled build graph cannot include yangpub.
package networkload

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangpub"
)

// Load reads path, validates it against the models in schemaDir, and returns
// the network tree encoded in it. An unreadable file, a file the schema
// rejects, a missing group-wide value, and a conflict between two providers
// are fatal: none of them has a safe default and none of them belongs to one
// entry. A defect inside one provider entry rejects that entry alone: the
// loader records it in Rejected, logs it, and returns the remaining providers.
// One provider's mistake never removes steering from the others. A document
// with no loadable provider is fatal.
func Load(path string, schemaDir string) (*networkjson.Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		slog.Error("networkjson: read failed", "err", err, "path", path)
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	schema, err := yangpub.LoadSchema(schemaDir)
	if err != nil {
		slog.Error("networkjson: schema load failed", "err", err, "schema_dir", schemaDir)
		return nil, fmt.Errorf("load schema from %s: %w", schemaDir, err)
	}
	defer schema.Close()
	if err := schema.ValidateConfigJSON(data); err != nil {
		slog.Error("networkjson: schema validation failed", "err", err, "path", path)
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	loaded, err := networkjson.Decode(data)
	if err != nil {
		var syntax *networkjson.SyntaxError
		if errors.As(err, &syntax) {
			slog.Error("networkjson: decode failed", "err", syntax.Unwrap(), "path", path)
			return nil, fmt.Errorf("decode %s: %w", path, syntax.Unwrap())
		}
		// build returns a missing group-wide value, a provider-set conflict (a
		// duplicate routing number, a reserved table), and a document with no
		// loadable provider. The wrapped error text states which.
		slog.Error("networkjson: configuration rejected", "err", err, "path", path)
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return loaded, nil
}

// ApplyFrom loads the network configuration at path, validates it against the
// models in schemaDir, and updates cfg only after loading succeeds.
func ApplyFrom(cfg *config.Config, path string, schemaDir string) error {
	loaded, err := Load(path, schemaDir)
	if err != nil {
		return err
	}
	loaded.Apply(cfg)
	return nil
}

// ApplyDefault loads /etc/mwan/network.json with schema models from
// /usr/local/share/wanconfig/yang.
func ApplyDefault(cfg *config.Config) error {
	return ApplyFrom(cfg, networkjson.DefaultPath, networkjson.DefaultSchemaDir)
}
