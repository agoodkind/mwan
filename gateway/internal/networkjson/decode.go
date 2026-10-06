package networkjson

import (
	"encoding/json"
	"log/slog"
)

// Load uses this error type to preserve the "decode <path>: <err>" format
// for JSON unmarshal failures.
type syntaxError struct {
	err error
}

func (e *syntaxError) Error() string {
	return "decode: " + e.err.Error()
}

func (e *syntaxError) Unwrap() error {
	return e.err
}

// Decode applies semantic checks without YANG schema validation.
// Decode reports provider entry rejections in Config.Rejected.
// Decode treats JSON null as an absent member.
func Decode(data []byte) (*Config, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		slog.Error("networkjson: decode failed", "err", err)
		return nil, &syntaxError{err: err}
	}
	return build(&doc)
}
