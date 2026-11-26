package ovs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"strings"

	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
)

const (
	multipartHeaderLength = 16
	flowDescriptionLength = 24

	flowDescriptionTable    = 4
	flowDescriptionPriority = 6
	flowDescriptionCookie   = 16
)

var errFlowDescription = errors.New("invalid OpenFlow multipart description")

var errForeignFlow = errors.New(
	"configured OpenFlow rule conflicts with a rule owned by another application",
)

type flowKey struct {
	cookie       uint64
	table        uint8
	priority     uint16
	match        string
	instructions string
}

func rulesMatch(
	ctx context.Context,
	conn net.Conn,
	wanted []*openflow15.FlowMod,
) (bool, error) {
	filter := openflow15.NewFlowStatsRequest()
	filter.TableId = openflow15.OFPTT_ALL

	expected := make(map[flowKey]int, len(wanted))

	identities := make(map[flowKey]bool, len(wanted))
	for _, flow := range wanted {
		key, err := ruleKey(flow.Cookie, flow.TableId, flow.Priority, flow.Match, flow.Instructions)
		if err != nil {
			return false, err
		}

		expected[key]++
		key.cookie, key.instructions = 0, ""
		identities[key] = true
	}

	matches := true
	remaining := len(wanted)
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

			if key.cookie&mwanFlowMask != mwanFlowCookie {
				key.cookie, key.instructions = 0, ""
				if identities[key] {
					return errForeignFlow
				}

				return nil
			}

			if expected[key] == 0 {
				matches = false

				return nil
			}

			expected[key]--
			remaining--

			return nil
		},
	)

	return matches && remaining == 0, err
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
		cookie:       binary.BigEndian.Uint64(description[flowDescriptionCookie:]),
		table:        description[flowDescriptionTable],
		priority:     binary.BigEndian.Uint16(description[flowDescriptionPriority:]),
		match:        strings.Join(fields, ""),
		instructions: string(instructions),
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
		cookie:       cookie,
		table:        table,
		priority:     priority,
		match:        strings.Join(fields, ""),
		instructions: actions.String(),
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
