package networkjson

// Both dataplanes enter the JSON decoder here. After parsing the common
// document, the builder selects Linux or OVS forwarding.

import "encoding/json"

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

// Decode checks network settings, treats JSON null as an absent member, and
// records rejected providers in Config.Rejected. Use networkload.Load to check
// a file against the YANG schema.
func Decode(data []byte) (*Config, error) {
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, &SyntaxError{err: err}
	}

	return build(&doc)
}
