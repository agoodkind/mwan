package ovs

import (
	"encoding/binary"
	"fmt"
	"log/slog"

	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
)

type bucketProgram struct {
	actions      []byte
	currentTable bool
	hasGroup     bool
	group        uint32
}

func (program bucketProgram) needsCaller(
	groups map[uint32][]bucketProgram,
	seen map[uint32]bool,
) bool {
	if program.currentTable {
		return true
	}

	if !program.hasGroup || seen[program.group] {
		return false
	}

	seen[program.group] = true
	for _, child := range groups[program.group] {
		if child.needsCaller(groups, seen) {
			return true
		}
	}

	return false
}

func actionBytes[T interface{ MarshalBinary() ([]byte, error) }](items []T) ([]byte, error) {
	var wire []byte

	for _, item := range items {
		data, err := item.MarshalBinary()
		if err != nil {
			slog.Error("OpenFlow action encoding failed", "err", err)

			return nil, fmt.Errorf("encode OpenFlow actions: %w", err)
		}

		wire = append(wire, data...)
	}

	return wire, nil
}

func applyWire(actions []byte) (*wireInstruction, error) {
	data := make([]byte, instructionSize, len(actions)+instructionSize)
	binary.BigEndian.PutUint16(data, openflow15.InstrType_APPLY_ACTIONS)
	data = append(data, actions...)
	length := len(data)
	if length > maxFlowSize {
		return nil, errFlowSize
	}

	binary.BigEndian.PutUint16(data[2:], uint16(length))

	return &wireInstruction{util.NewBuffer(data)}, nil
}
