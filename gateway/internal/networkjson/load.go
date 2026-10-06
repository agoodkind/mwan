//go:build cgo

package networkjson

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/mwan/internal/config"
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
func Load(path string, schemaDir string) (*Config, error) {
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
	loaded, err := Decode(data)
	if err != nil {
		var syntax *syntaxError
		if errors.As(err, &syntax) {
			slog.Error("networkjson: decode failed", "err", syntax.err, "path", path)
			return nil, fmt.Errorf("decode %s: %w", path, syntax.err)
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
// models in schemaDir, and writes it onto cfg. Every process that reads the
// network tree goes through here rather than repeating the sequence, so one
// file owns each value and one implementation decides what a bad file means.
// cfg is left untouched when the load fails, so a caller that carries on with a
// diagnostic never shows a half-filled tree.
func ApplyFrom(cfg *config.Config, path string, schemaDir string) error {
	loaded, err := Load(path, schemaDir)
	if err != nil {
		return err
	}
	loaded.Apply(cfg)
	return nil
}

// ApplyDefault applies the network configuration from the paths the deploy
// installs.
func ApplyDefault(cfg *config.Config) error {
	return ApplyFrom(cfg, DefaultPath, DefaultSchemaDir)
}
