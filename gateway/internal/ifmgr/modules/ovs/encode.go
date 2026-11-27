// Package ovs configures Open vSwitch bridges, ports, and OpenFlow rules.
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
	mwanFlowCookie  = ovsconfig.FlowCookiePrefix
	mwanFlowMask    = 0xffffffff00000000
	flowHeaderSize  = 48
	oxmHeaderSize   = 4
	wireAlignment   = 8
	instructionSize = 8
	bundleOverhead  = 16
	maxFlowSize     = math.MaxUint16 - bundleOverhead
)

var (
	errUnknownFlowPort    = errors.New("rule references an OVS port without an OpenFlow number")
	errInvalidFlowTable   = errors.New("a rule must select a specific OpenFlow table")
	errFlowSize           = errors.New("rule is too large for an OpenFlow message")
	errEncodedOXM         = errors.New("encoded match must contain one complete OpenFlow OXM field")
	errEncodedInstruction = errors.New(
		"encoded instruction must contain one complete OpenFlow instruction",
	)
	errFlowActions = errors.New("cannot append actions to an encoded OpenFlow instruction")
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
	flow.Cookie = rule.Cookie()
	flow.Priority = rule.Priority
	flow.TableId = rule.Table
	match, err := marshalMatchFields(rule.Match, numbers)
	if err != nil {
		return nil, err
	}

	for _, raw := range match {
		if int(flow.Match.Len())+len(raw) > maxFlowSize-flowHeaderSize-wireAlignment {
			return nil, errFlowSize
		}

		field, err := rawOXM(raw)
		if err != nil {
			return nil, err
		}

		flow.Match.AddField(field)
	}

	instructions, err := marshalActionFields(instructionExpression, rule.Actions, numbers)
	if err != nil {
		return nil, err
	}

	if err := marshalInstructions(flow, instructions); err != nil {
		return nil, err
	}

	wire, err := flow.MarshalBinary()
	if err != nil {
		slog.ErrorContext(ctx, "OpenFlow flow encoding failed", "err", err)

		return nil, fmt.Errorf("marshal flow for OVS validation: %w", err)
	}

	if err := validateFlowWire(wire); err != nil {
		return nil, err
	}

	return flow, nil
}

func marshalInstructions(
	flow *openflow15.FlowMod,
	instructions [][]byte,
) error {
	for _, raw := range instructions {
		if len(raw) < instructionSize || len(raw)%wireAlignment != 0 ||
			int(binary.BigEndian.Uint16(raw[2:4])) != len(raw) {
			return errEncodedInstruction
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
		return openflow15.MatchField{}, errEncodedOXM
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
