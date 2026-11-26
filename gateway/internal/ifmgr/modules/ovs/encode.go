package ovs

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

const (
	mwanFlowCookie  = 0x6d77616e00000000
	mwanFlowMask    = 0xffffffff00000000
	flowHeaderSize  = 48
	oxmHeaderSize   = 4
	wireAlignment   = 8
	instructionSize = 8
	bundleOverhead  = 16
	maxFlowSize     = math.MaxUint16 - bundleOverhead
)

var (
	errUnknownFlowPort  = errors.New("rule references an OVS port without an OpenFlow number")
	errInvalidFlowTable = errors.New("a rule must select a specific OpenFlow table")
	errFlowSize         = errors.New("rule is too large for an OpenFlow message")
	errRawOXM           = errors.New("raw match must contain one complete OpenFlow OXM field")
	errRawInstruction   = errors.New(
		"raw instruction must contain one complete OpenFlow instruction",
	)
	errFlowActions = errors.New("use output ports or raw instructions in each rule")
)

func marshalFlow(
	ctx context.Context,
	rule ovsconfig.Flow,
	numbers map[string]uint32,
) (*openflow15.FlowMod, error) {
	if rule.Table == openflow15.OFPTT_ALL {
		return nil, errInvalidFlowTable
	}

	flow := openflow15.NewFlowMod()
	flow.Cookie = mwanFlowCookie
	flow.Priority = rule.Priority
	flow.TableId = rule.Table
	if rule.InPort != "" {
		number, exists := numbers[rule.InPort]
		if !exists {
			slog.ErrorContext(
				ctx,
				"OpenFlow input port unavailable",
				"port",
				rule.InPort,
				"err",
				errUnknownFlowPort,
			)

			return nil, fmt.Errorf("%w: %q", errUnknownFlowPort, rule.InPort)
		}

		flow.Match.AddField(*openflow15.NewInPortField(number))
	}

	if rule.EtherType != nil {
		flow.Match.AddField(*openflow15.NewEthTypeField(*rule.EtherType))
	}

	for _, raw := range rule.RawMatch {
		if int(flow.Match.Len())+len(raw) > maxFlowSize-flowHeaderSize-wireAlignment {
			return nil, errFlowSize
		}

		field, err := rawOXM(raw)
		if err != nil {
			return nil, err
		}

		flow.Match.AddField(field)
	}

	if err := marshalInstructions(ctx, flow, rule, numbers); err != nil {
		return nil, err
	}

	return flow, nil
}

func marshalInstructions(
	ctx context.Context,
	flow *openflow15.FlowMod,
	rule ovsconfig.Flow,
	numbers map[string]uint32,
) error {
	if len(rule.Output) > 0 {
		if len(rule.RawInstruction) > 0 {
			return errFlowActions
		}

		instruction := openflow15.NewInstrApplyActions()

		for _, port := range rule.Output {
			number, exists := numbers[port]
			if !exists {
				slog.ErrorContext(
					ctx,
					"OpenFlow output port unavailable",
					"port",
					port,
					"err",
					errUnknownFlowPort,
				)

				return fmt.Errorf("%w: %q", errUnknownFlowPort, port)
			}

			action := openflow15.NewActionOutput(number)
			action.MaxLen = 0
			if int(flow.Len())+int(instruction.Len())+int(action.Len()) > maxFlowSize {
				return errFlowSize
			}

			instruction.Actions = append(instruction.Actions, action)
		}

		instruction.Length = instruction.Len()
		flow.AddInstruction(instruction)
	}

	for _, raw := range rule.RawInstruction {
		if len(raw) < instructionSize || len(raw)%wireAlignment != 0 ||
			int(binary.BigEndian.Uint16(raw[2:4])) != len(raw) {
			return errRawInstruction
		}

		if int(flow.Len())+len(raw) > maxFlowSize {
			return errFlowSize
		}

		flow.AddInstruction(wireInstruction{Buffer: util.NewBuffer(raw)})
	}

	return nil
}

func rawOXM(raw []byte) (openflow15.MatchField, error) {
	if len(raw) <= oxmHeaderSize || int(raw[3]) != len(raw)-oxmHeaderSize {
		return openflow15.MatchField{}, errRawOXM
	}

	// Preserve OXM payload bytes, including any experimenter ID or mask.
	return openflow15.MatchField{
		Class:          binary.BigEndian.Uint16(raw[:2]),
		Field:          raw[2] >> 1,
		HasMask:        raw[2]&1 != 0,
		Length:         raw[3],
		ExperimenterID: 0,
		Value:          util.NewBuffer(raw[oxmHeaderSize:]),
		Mask:           util.NewBuffer(nil),
	}, nil
}

type wireInstruction struct {
	*util.Buffer
}

func (wireInstruction) AddAction(_ openflow15.Action, _ bool) error {
	return errFlowActions
}
