//go:build !linux || !cgo

package ovs

import "errors"

var errTextPlatform = errors.New("OpenFlow expressions require a Linux build with libopenvswitch")

func validateFlowWire(_ []byte) error {
	return errTextPlatform
}

func marshalText(_ expressionType, _ string, _ map[string]uint32) ([]byte, error) {
	return nil, errTextPlatform
}
