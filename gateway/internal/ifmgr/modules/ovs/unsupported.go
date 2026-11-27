//go:build !linux || !cgo

package ovs

// On builds without Linux or cgo, these functions report that OpenFlow
// expressions require a Linux build with libopenvswitch. Expression parsing,
// flow validation, and action inspection all return that error.

import "errors"

var errTextPlatform = errors.New("OpenFlow expressions require a Linux build with libopenvswitch")

func validateFlowWire(_ []byte) error {
	return errTextPlatform
}

func marshalText(_ expressionType, _ string, _ map[string]uint32) ([]byte, error) {
	return nil, errTextPlatform
}

func inspectActions(_ []byte, _ bool, _ *[256]byte) (bucketProgram, error) {
	return bucketProgram{}, errTextPlatform
}
