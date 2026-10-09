// Package networkload validates network documents with libyang before
// semantic decoding.
package networkload

import (
	"errors"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/yangschema"
)

// JSONError wraps errors from [networkjson.Canonicalize] for [errors.As] matching.
type JSONError struct {
	err error
}

func (e *JSONError) Error() string {
	return "decode: " + e.err.Error()
}

func (e *JSONError) Unwrap() error {
	return e.err
}

// SchemaError wraps schema validation errors for [errors.As] matching.
// SchemaError wraps [yangschema.ErrSchemaClosed] for nil or closed schemas.
type SchemaError struct {
	err error
}

func (e *SchemaError) Error() string {
	return "validate: " + e.err.Error()
}

func (e *SchemaError) Unwrap() error {
	return e.err
}

// ValidateAndDecode checks JSON, validates the document, then decodes its semantics.
// Each stage reads the original bytes.
// ValidateAndDecode returns errors from [networkjson.Decode] without wrapping them.
func ValidateAndDecode(data []byte, schema *yangschema.Schema) (*networkjson.Config, error) {
	if _, err := networkjson.Canonicalize(data); err != nil {
		return nil, &JSONError{err: err}
	}
	if err := schema.ValidateConfigJSON(data); err != nil {
		return nil, &SchemaError{err: err}
	}
	return networkjson.Decode(data)
}

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
	schema, err := yangschema.LoadSchema(schemaDir)
	if err != nil {
		slog.Error("networkjson: schema load failed", "err", err, "schema_dir", schemaDir)
		return nil, fmt.Errorf("load schema from %s: %w", schemaDir, err)
	}
	defer schema.Close()
	loaded, err := ValidateAndDecode(data, schema)
	if err != nil {
		return nil, loadFailed(path, err)
	}
	return loaded, nil
}

func loadFailed(path string, err error) error {
	var rejected *SchemaError
	if errors.As(err, &rejected) {
		slog.Error("networkjson: schema validation failed", "err", rejected.Unwrap(), "path", path)
		return fmt.Errorf("validate %s: %w", path, rejected.Unwrap())
	}
	var malformed *JSONError
	if errors.As(err, &malformed) {
		slog.Error("networkjson: decode failed", "err", malformed.Unwrap(), "path", path)
		return fmt.Errorf("decode %s: %w", path, malformed.Unwrap())
	}
	var syntax *networkjson.SyntaxError
	if errors.As(err, &syntax) {
		slog.Error("networkjson: decode failed", "err", syntax.Unwrap(), "path", path)
		return fmt.Errorf("decode %s: %w", path, syntax.Unwrap())
	}
	// build returns a missing group-wide value, a provider-set conflict (a
	// duplicate routing number, a reserved table), and a document with no
	// loadable provider. The wrapped error text states which.
	slog.Error("networkjson: configuration rejected", "err", err, "path", path)
	return fmt.Errorf("%s: %w", path, err)
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
