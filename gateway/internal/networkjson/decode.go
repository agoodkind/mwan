package networkjson

import (
	"encoding/json"
)

// SyntaxError supports errors.As matching in [networkload.Load] for the
// "decode <path>: <err>" error format.
type SyntaxError struct {
	err error
}

func (e *SyntaxError) Error() string {
	return "decode: " + e.err.Error()
}

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
