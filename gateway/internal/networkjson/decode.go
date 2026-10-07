package networkjson

import (
	"encoding/json"
)

// SyntaxError supports [errors.As] matching in [networkload.Load] for the
// "decode <path>: <err>" error format.
type SyntaxError struct {
	err error
}

// Error prefixes the JSON error with "decode: ".
func (e *SyntaxError) Error() string {
	return "decode: " + e.err.Error()
}

// Unwrap returns the JSON error for [networkload.Load] to format with the path.
func (e *SyntaxError) Unwrap() error {
	return e.err
}

// Decode applies semantic checks without YANG schema validation.
// Decode reports provider entry rejections in Config.Rejected.
// Decode treats JSON null as an absent member.
func Decode(data []byte) (*Config, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, &SyntaxError{err: err}
	}
	return build(&doc)
}
