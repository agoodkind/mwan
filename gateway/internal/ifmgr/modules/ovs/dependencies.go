package ovs

import (
	"encoding/binary"
)

func inspectGroupTables(record []byte, installed *installedEntries, groupID uint32) error {
	if len(record) < groupDescriptionLength {
		return errFlowDescription
	}

	end := groupDescriptionLength + int(binary.BigEndian.Uint16(record[8:]))
	if end > len(record) {
		return errFlowDescription
	}

	for data := record[groupDescriptionLength:end]; len(data) > 0; {
		if len(data) < bucketHeaderLength {
			return errFlowDescription
		}

		size := int(binary.BigEndian.Uint16(data))
		actionsEnd := bucketHeaderLength + int(binary.BigEndian.Uint16(data[2:]))
		if size < actionsEnd || size > len(data) {
			return errFlowDescription
		}

		program, err := inspectActions(
			data[bucketHeaderLength:actionsEnd],
			true,
			&installed.references,
		)
		if err != nil {
			return err
		}

		installed.dependencies[groupID] = append(installed.dependencies[groupID], program)
		data = data[size:]
	}

	return nil
}
