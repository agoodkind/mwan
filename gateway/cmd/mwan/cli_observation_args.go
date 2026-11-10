package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"goodkind.io/mwan/internal/observation"
)

type singleObservationInput struct {
	check observation.CheckSpec
	paths []observation.PathIdentity
}

func prepareSingleObservation(checkJSON, pathsJSON string, input io.Reader) (singleObservationInput, error) {
	var prepared singleObservationInput
	if len(checkJSON) > 64*1024 || len(pathsJSON) > 64*1024 {
		return prepared, fmt.Errorf("observe JSON input exceeds 64 KiB")
	}
	pathDecoder := json.NewDecoder(strings.NewReader(pathsJSON))
	pathDecoder.DisallowUnknownFields()
	if err := pathDecoder.Decode(&prepared.paths); err != nil {
		return prepared, fmt.Errorf("observe requires valid path identities JSON")
	}
	var remaining json.RawMessage
	if err := pathDecoder.Decode(&remaining); err != io.EOF {
		return prepared, fmt.Errorf("observe requires exactly one path identities JSON array")
	}
	if checkJSON == "" {
		input = io.LimitReader(input, 64*1024)
	} else {
		input = strings.NewReader(checkJSON)
	}
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&prepared.check); err != nil {
		return prepared, fmt.Errorf("observe requires a valid CheckSpec JSON object")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return prepared, fmt.Errorf("observe requires exactly one CheckSpec JSON object")
	}
	return prepared, nil
}
