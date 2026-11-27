package ovs

// OpenFlow readback gives the installer the current flows and groups for
// comparison. A configured rule conflicts with an installed rule when the
// table, priority, and match agree but another application owns the cookie.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strings"

	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
)

const (
	multipartHeaderLength   = 16
	flowDescriptionLength   = 24
	groupDescriptionLength  = 16
	bucketHeaderLength      = 8
	propertyHeaderLength    = 4
	flowDescriptionTable    = 4
	flowDescriptionPriority = 6
	flowDescriptionCookie   = 16
)

var errFlowDescription = errors.New("invalid OpenFlow multipart description")

var errForeignFlow = errors.New(
	"configured OpenFlow rule conflicts with a rule owned by another application",
)

type flowIdentity struct {
	table    uint8
	priority uint16
	match    string
}

type flowContents struct {
	cookie       uint64
	instructions string
}

type flowKey struct {
	identity flowIdentity
	contents flowContents
}

type installedGroup struct {
	key     string
	buckets map[uint32]bool
}

type installedEntries struct {
	dependencies map[uint32][]bucketProgram
	references   [256]byte
	flows        map[flowIdentity]flowContents
	groups       map[uint32]installedGroup
}

func readGroups(
	ctx context.Context,
	conn net.Conn,
	translations *installedEntries,
) (map[uint32]installedGroup, error) {
	current := make(map[uint32]installedGroup)
	filter := openflow15.NewGroupMultipartRequest(openflow15.OFPG_ALL)
	err := readDescriptions(
		ctx,
		conn,
		openflow15.MultipartType_GroupDesc,
		filter,
		groupDescriptionLength,
		func(record []byte) error {
			groupID := binary.BigEndian.Uint32(record[4:8])
			if groupID&groupMask == groupNamespace {
				buckets := make(map[uint32]bool)
				key, err := groupDescriptionKey(record, buckets)
				if err != nil {
					return err
				}

				current[groupID] = installedGroup{key: key, buckets: buckets}
			} else if translations != nil {
				return inspectGroupTables(record, translations, groupID)
			}

			return nil
		},
	)

	return current, err
}

func groupChanges(
	current map[uint32]installedGroup,
	desired []*openflow15.GroupMod,
) ([]*openflow15.GroupMod, error) {
	remaining := maps.Clone(current)
	var changes []*openflow15.GroupMod

	for _, group := range desired {
		change := *group
		change.Command = openflow15.OFPGC_ADD
		existing, found := remaining[group.GroupId]
		delete(remaining, group.GroupId)
		if found {
			desc := openflow15.NewGroupDesc()
			desc.GroupId, desc.Type = group.GroupId, group.Type

			desc.Properties = group.Properties
			for _, bucket := range group.Buckets {
				desc.AddBucket(bucket)
			}

			wire, err := marshalComparison(desc)
			if err != nil {
				return nil, err
			}

			key, err := groupDescriptionKey(wire, nil)
			if err != nil {
				return nil, err
			}

			if existing.key == key {
				continue
			}

			change.Command = openflow15.OFPGC_MODIFY
		}

		changes = append(changes, &change)
	}

	for _, id := range slices.Sorted(maps.Keys(remaining)) {
		remove := openflow15.NewGroupMod()
		remove.Command = openflow15.OFPGC_DELETE
		remove.GroupId = id
		changes = append(changes, remove)
	}

	return changes, nil
}

func groupDescriptionKey(record []byte, bucketIDs map[uint32]bool) (string, error) {
	if len(record) < groupDescriptionLength || len(record) != int(binary.BigEndian.Uint16(record)) {
		return "", errFlowDescription
	}

	end := groupDescriptionLength + int(binary.BigEndian.Uint16(record[8:]))
	if end > len(record) {
		return "", errFlowDescription
	}

	key := slices.Clone(record[:groupDescriptionLength])
	var removedTotal uint16

	buckets := record[groupDescriptionLength:end]
	for len(buckets) > 0 {
		if len(buckets) < bucketHeaderLength {
			return "", errFlowDescription
		}

		size := int(binary.BigEndian.Uint16(buckets))
		actionsEnd := bucketHeaderLength + int(binary.BigEndian.Uint16(buckets[2:]))
		if actionsEnd > size || size > len(buckets) {
			return "", errFlowDescription
		}

		if bucketIDs != nil {
			bucketIDs[binary.BigEndian.Uint32(buckets[4:8])] = true
		}

		start := len(key)
		key = append(key, buckets[:actionsEnd]...)
		properties, removed, err := bucketPropertyKeys(record[2], buckets[actionsEnd:size])
		if err != nil {
			return "", err
		}

		for _, property := range properties {
			key = append(key, property...)
		}

		removedTotal += removed
		binary.BigEndian.PutUint16(key[start:], binary.BigEndian.Uint16(buckets)-removed)
		buckets = buckets[size:]
	}

	binary.BigEndian.PutUint16(key[8:], binary.BigEndian.Uint16(record[8:])-removedTotal)
	key = append(key, record[end:]...)
	binary.BigEndian.PutUint16(key, binary.BigEndian.Uint16(record)-removedTotal)

	return string(key), nil
}

func bucketPropertyKeys(groupType uint8, wire []byte) ([]string, uint16, error) {
	var properties []string
	var removed uint16

	for len(wire) > 0 {
		if len(wire) < propertyHeaderLength {
			return nil, 0, errFlowDescription
		}

		size := int(binary.BigEndian.Uint16(wire[2:]))
		padded := (size + wireAlignment - 1) &^ (wireAlignment - 1)
		if size < propertyHeaderLength || padded > len(wire) {
			return nil, 0, errFlowDescription
		}

		if defaultBucketProperty(groupType, wire[:size]) {
			removed += wireAlignment
		} else {
			properties = append(properties, string(wire[:padded]))
		}

		wire = wire[padded:]
	}

	slices.Sort(properties)

	return properties, removed, nil
}

// OVS emits SELECT weight 1 and omits unrestricted port and group watches.
func defaultBucketProperty(groupType uint8, property []byte) bool {
	if len(property) != wireAlignment {
		return false
	}

	switch binary.BigEndian.Uint16(property) {
	case openflow15.GBPT_WEIGHT:
		weight := binary.BigEndian.Uint16(property[4:])
		if groupType == openflow15.GT_SELECT {
			return weight == 1
		}

		return weight == 0
	case openflow15.GBPT_WATCH_PORT:
		return binary.BigEndian.Uint32(property[4:]) == openflow15.P_ANY
	case openflow15.GBPT_WATCH_GROUP:
		return binary.BigEndian.Uint32(property[4:]) == openflow15.OFPG_ANY
	}

	return false
}

func readFlows(ctx context.Context, conn net.Conn) (map[flowIdentity]flowContents, error) {
	filter := openflow15.NewFlowStatsRequest()
	filter.TableId = openflow15.OFPTT_ALL

	current := make(map[flowIdentity]flowContents)
	err := readDescriptions(
		ctx,
		conn,
		openflow15.MultipartType_FlowDesc,
		filter,
		flowDescriptionLength,
		func(record []byte) error {
			key, err := readFlowKey(record)
			if err != nil {
				return err
			}

			current[key.identity] = key.contents

			return nil
		},
	)

	return current, err
}

func rulesMatch(current map[flowIdentity]flowContents, wanted []*openflow15.FlowMod) (bool, error) {
	var managed int

	for _, flow := range current {
		if flow.cookie&mwanFlowMask == mwanFlowCookie {
			managed++
		}
	}

	matches := managed == len(wanted)

	for _, flow := range wanted {
		key, err := ruleKey(flow.Cookie, flow.TableId, flow.Priority, flow.Match, flow.Instructions)
		if err != nil {
			return false, err
		}

		installed, found := current[key.identity]
		if found && installed.cookie&mwanFlowMask != mwanFlowCookie {
			return false, errForeignFlow
		}

		if !found || installed != key.contents {
			matches = false
		}
	}

	return matches, nil
}

// Preserve vendor instructions and actions that libOpenflow cannot decode.
func readDescriptions(
	ctx context.Context,
	conn net.Conn,
	kind uint16,
	filter util.Message,
	minimum int,
	visit func([]byte) error,
) error {
	request := new(openflow15.MultipartRequest)
	request.Header = openflow15.NewOfp15Header()
	request.Header.Type = openflow15.Type_MultiPartRequest
	request.Type = kind
	request.Body = []util.Message{filter}
	if err := sendOpenFlow(ctx, conn, request); err != nil {
		return err
	}

	for {
		frame, err := receiveOpenFlow(ctx, conn, openflow15.Type_MultiPartReply, request.Xid)
		if err != nil {
			return err
		}

		if len(frame) < multipartHeaderLength ||
			binary.BigEndian.Uint16(frame[flowHeaderLength:]) != kind {
			return errFlowDescription
		}

		body := frame[multipartHeaderLength:]
		for len(body) > 0 {
			if len(body) < minimum {
				return errFlowDescription
			}

			size := int(binary.BigEndian.Uint16(body))
			if size < minimum || size > len(body) {
				return errFlowDescription
			}

			if err := visit(body[:size]); err != nil {
				return err
			}

			body = body[size:]
		}

		if binary.BigEndian.Uint16(frame[flowHeaderLength+2:])&openflow15.OFPMPF_REPLY_MORE == 0 {
			return nil
		}
	}
}

func readFlowKey(description []byte) (flowKey, error) {
	var empty flowKey

	match, tail, err := splitFlowFields(description[flowDescriptionLength:])
	if err != nil {
		return empty, err
	}

	_, instructions, err := splitFlowFields(tail)
	if err != nil {
		return empty, err
	}

	var fields []string

	for len(match) != 0 {
		if len(match) < oxmHeaderSize {
			return empty, errFlowDescription
		}

		length := oxmHeaderSize + int(match[oxmHeaderSize-1])
		if length > len(match) {
			return empty, errFlowDescription
		}

		fields = append(fields, string(match[:length]))
		match = match[length:]
	}

	slices.Sort(fields)

	return flowKey{
		identity: flowIdentity{
			table:    description[flowDescriptionTable],
			priority: binary.BigEndian.Uint16(description[flowDescriptionPriority:]),
			match:    strings.Join(fields, ""),
		},
		contents: flowContents{
			cookie:       binary.BigEndian.Uint64(description[flowDescriptionCookie:]),
			instructions: string(instructions),
		},
	}, nil
}

// OpenFlow pads match and statistics blocks to eight bytes.
func splitFlowFields(data []byte) ([]byte, []byte, error) {
	const headerLength = 4

	if len(data) < headerLength {
		return nil, nil, errFlowDescription
	}

	length := int(binary.BigEndian.Uint16(data[2:headerLength]))
	aligned := (length + wireAlignment - 1) &^ (wireAlignment - 1)
	if length < headerLength || aligned > len(data) {
		return nil, nil, errFlowDescription
	}

	return data[headerLength:length], data[aligned:], nil
}

func ruleKey(
	cookie uint64,
	table uint8,
	priority uint16,
	match openflow15.Match,
	instructions []openflow15.Instruction,
) (flowKey, error) {
	var empty flowKey

	fields := make([]string, 0, len(match.Fields))
	for _, field := range match.Fields {
		encoded, err := marshalComparison(&field)
		if err != nil {
			return empty, err
		}

		fields = append(fields, string(encoded))
	}

	slices.Sort(fields)

	var actions strings.Builder

	for _, instruction := range instructions {
		encoded, err := marshalComparison(instruction)
		if err != nil {
			return empty, err
		}

		actions.Write(encoded)
	}

	return flowKey{
		identity: flowIdentity{table: table, priority: priority, match: strings.Join(fields, "")},
		contents: flowContents{cookie: cookie, instructions: actions.String()},
	}, nil
}

func marshalComparison(message util.Message) ([]byte, error) {
	encoded, err := message.MarshalBinary()
	if err != nil {
		slog.Error(
			"OpenFlow comparison encoding failed",
			"type",
			fmt.Sprintf("%T", message),
			"err",
			err,
		)

		return nil, fmt.Errorf("encode %T for OpenFlow comparison: %w", message, err)
	}

	return encoded, nil
}
