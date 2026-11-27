//go:build linux && cgo

package ovs

/*
#cgo CFLAGS: -I/opt/parser/include -I/opt/parser/include/include -I/opt/parser/include/lib
#cgo LDFLAGS: -L/opt/parser/lib -lopenvswitch -latomic -lm
#include <stdlib.h>
#include "text.h"
*/
import "C"

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unsafe"
)

func validateFlowWire(data []byte) error {
	message := C.mwan_validate_flow((*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(data))))
	if message != nil {
		err := fmt.Errorf("%w: %s", errExpression, C.GoString(message))
		slog.Error("OVS rejected the match and actions", "err", err)

		return err
	}

	return nil
}

func marshalText(
	kind expressionType,
	expression string,
	numbers map[string]uint32,
) ([]byte, error) {
	if strings.IndexByte(expression, 0) >= 0 {
		return nil, errTextNUL
	}
	ports := C.mwan_ports_new()
	defer C.mwan_ports_free(ports)
	for name, number := range numbers {
		if strings.IndexByte(name, 0) >= 0 {
			return nil, errTextNUL
		}
		port := C.CString(name)
		status := C.mwan_port_add(ports, C.uint32_t(number), port)
		C.mwan_string_free(port)
		if status != 0 {
			slog.Error(
				"OVS port number rejected",
				"port",
				name,
				"number",
				number,
				"err",
				errUnknownFlowPort,
			)

			return nil, fmt.Errorf("%w: %s (%d)", errUnknownFlowPort, name, number)
		}
	}
	text := C.CString(expression)
	defer C.mwan_string_free(text)
	wire := C.mwan_parse(ports, C.int(kind), text, groupNamespace, maxGroupID)
	if wire.error != nil {
		defer C.mwan_string_free(wire.error)

		return nil, fmt.Errorf("%w: %s", errExpression, C.GoString(wire.error))
	}
	defer C.mwan_string_free(wire.data)
	if wire.size > maxFlowSize {
		return nil, errFlowSize
	}

	return slices.Clone(unsafe.Slice((*byte)(unsafe.Pointer(wire.data)), int(wire.size))), nil
}

func inspectActions(data []byte, bucket bool, tables *[256]byte) (bucketProgram, error) {
	if len(data) == 0 {
		var empty bucketProgram

		return empty, nil
	}
	var actions C.int
	if bucket {
		actions = 1
	}
	wire := C.mwan_inspect(
		(*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(data))),
		C.size_t(len(data)),
		actions,
		(*C.uint8_t)(unsafe.Pointer(&tables[0])),
	)
	if wire.error != nil {
		defer C.mwan_string_free(wire.error)

		detail := fmt.Errorf("%w: %s", errExpression, C.GoString(wire.error))
		slog.Error("OpenFlow action decoding failed", "err", detail)

		return bucketProgram{}, detail
	}
	defer C.mwan_string_free(wire.data)

	return bucketProgram{
		actions: slices.Clone(
			unsafe.Slice((*byte)(unsafe.Pointer(wire.data)), int(wire.size)),
		),
		currentTable: wire.current_table != 0,
		hasGroup:     wire.has_group != 0,
		group:        uint32(wire.target_group),
	}, nil
}
