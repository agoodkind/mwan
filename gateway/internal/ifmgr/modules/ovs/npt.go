package ovs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/bits"
	"net/netip"

	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/nptpolicy"
)

var (
	errNPTTables = errors.New("insufficient unused OpenFlow tables for IPv6 prefix translation")
	errNPTPrefix = errors.New("IPv6 prefix translation requires IPv6 prefixes of at most 64 bits")
)

const (
	nptPriority      = 100
	invalidPriority  = nptPriority + 8
	wordModulus      = math.MaxUint16
	wordRange        = wordModulus + 1
	nibbleWidth      = 4
	nibbleMask       = (1 << nibbleWidth) - 1
	wordBits         = 16
	subnetPrefixBits = 48
	iidWords         = 4
	bitsPerByte      = 8
)

type nptCompiler struct {
	used       [256]bool
	groups     map[uint32][]bucketProgram
	helpers    []*openflow15.FlowMod
	clearTable uint8
}

func (c *nptCompiler) table() (uint8, error) {
	for i := 253; i > 0; i-- {
		if !c.used[i] {
			c.used[i] = true

			return uint8(i), nil
		}
	}

	return 0, errNPTTables
}

func (c *nptCompiler) rule(table uint8, priority uint16, match, actions string) error {
	flow := openflow15.NewFlowMod()
	flow.TableId, flow.Priority, flow.Cookie = table, priority, mwanFlowCookie
	fields, err := marshalTextFields(matchExpression, match, nil)
	if err != nil {
		return err
	}

	for _, field := range fields {
		oxm, err := rawOXM(field)
		if err != nil {
			return err
		}

		flow.Match.AddField(oxm)
	}

	instructions, err := marshalTextFields(instructionExpression, actions, nil)
	if err != nil {
		slog.Error("NPT action encoding failed", "err", err)

		return fmt.Errorf("compile NPT actions %q: %w", actions, err)
	}

	for _, instruction := range instructions {
		flow.AddInstruction(wireInstruction{util.NewBuffer(instruction)})
	}

	c.helpers = append(c.helpers, flow)

	return nil
}

func wordMatch(p netip.Prefix, prior int, long bool, fill byte, first uint16) string {
	addr := p.Masked().Addr().As16()
	var mask [16]byte

	for i := range p.Bits() {
		mask[i/8] |= 1 << (bitsPerByte - 1 - i%bitsPerByte)
	}

	start := 6
	if long {
		start = 8
	}

	for i := start; i < start+2*prior; i++ {
		addr[i], mask[i] = fill, 255
	}

	if first != 0 {
		binary.BigEndian.PutUint16(addr[start:], first)
	}

	return fmt.Sprintf("%s/%s", netip.AddrFrom16(addr), netip.AddrFrom16(mask))
}

func (c *nptCompiler) mapping(mapping config.NPTv6Translation) (uint8, error) {
	internal, external := mapping.InternalPrefix, mapping.ExternalPrefix
	if !internal.IsValid() || !internal.Addr().Is6() || internal.Bits() > 64 {
		return 0, errNPTPrefix
	}

	entry, err := c.table()
	if err != nil {
		return 0, err
	}

	if !external.IsValid() && mapping.ExternalSource == config.PrefixDelegated {
		return entry, c.reject(entry, nptPriority, "ipv6,ipv6_src="+internal.String())
	}

	if !external.IsValid() || !external.Addr().Is6() || external.Bits() > 64 {
		return 0, errNPTPrefix
	}

	var stages [6]uint8
	for i := range stages {
		stages[i], err = c.table()
		if err != nil {
			return 0, err
		}
	}

	// Pad the shorter prefix with zero bits before calculating the adjustment.
	width := max(internal.Bits(), external.Bits())
	internal = netip.PrefixFrom(internal.Masked().Addr(), width)
	external = netip.PrefixFrom(external.Masked().Addr(), width)
	var directions [2]uint8

	for direction := range 2 {
		dispatch, err := c.table()
		if err != nil {
			return 0, err
		}

		directions[direction] = dispatch
		if err := c.direction(
			dispatch,
			stages,
			direction,
			internal,
			external,
			mapping,
		); err != nil {
			return 0, err
		}
	}

	return entry, c.rule(
		entry,
		1,
		"ipv6",
		fmt.Sprintf("resubmit(,%d),resubmit(,%d)", directions[0], directions[1]),
	)
}

func (c *nptCompiler) direction(
	dispatch uint8,
	stages [6]uint8,
	direction int,
	internal netip.Prefix,
	external netip.Prefix,
	mapping config.NPTv6Translation,
) error {
	width := internal.Bits()
	source, dest := internal, external
	field, register := "ipv6_src", "NXM_NX_IPV6_SRC"
	if direction == 1 {
		source, dest = dest, source
		field, register = "ipv6_dst", "NXM_NX_IPV6_DST"
	}

	delta := int(nptpolicy.Adjustment(source, dest)) % wordModulus
	original := mapping.InternalPrefix
	if direction == 1 {
		original = mapping.ExternalPrefix
	}

	if original.Bits() < width {
		if err := c.reject(dispatch, 1, "ipv6,"+field+"="+original.String()); err != nil {
			return err
		}
	}

	offsets := []int{64}
	if width > subnetPrefixBits {
		offsets = []int{48, 32, 16, 0}
		if err := c.rejectZeroIID(dispatch, field, source, delta); err != nil {
			return err
		}
	}

	for index, offset := range offsets {
		key := direction*iidWords + index
		match := "ipv6," + field + "=" + wordMatch(
			source,
			index,
			width > subnetPrefixBits,
			math.MaxUint8,
			0,
		)
		actions := fmt.Sprintf(
			"push:NXM_NX_REG0[],"+
				"push:NXM_NX_REG1[],"+
				"push:NXM_NX_REG2[],"+
				"push:NXM_NX_REG3[],"+
				"move:%s[%d..%d]->NXM_NX_REG0[0..15],load:%d->NXM_NX_REG2[],load:%d->NXM_NX_REG3[],"+
				"set_field:%s/%d->%s,resubmit(,%d),"+
				"pop:NXM_NX_REG3[],pop:NXM_NX_REG2[],pop:NXM_NX_REG1[],pop:NXM_NX_REG0[]",
			register,
			offset,
			offset+wordBits-1,
			key,
			direction,
			dest.Addr(),
			width,
			field,
			stages[0],
		)
		if err := c.rule(dispatch, uint16(nptPriority+index), match, actions); err != nil {
			return err
		}

		if err := c.rule(
			stages[5],
			nptPriority,
			fmt.Sprintf("ipv6,reg2=%d", key),
			fmt.Sprintf("move:NXM_NX_REG0[0..15]->%s[%d..%d]", register, offset, offset+wordBits-1),
		); err != nil {
			return err
		}
	}

	if err := c.reject(
		dispatch,
		invalidPriority,
		"ipv6,"+field+"="+wordMatch(
			source,
			len(offsets),
			width > subnetPrefixBits,
			math.MaxUint8,
			0,
		),
	); err != nil {
		return err
	}

	if err := c.arithmetic(stages, direction, delta); err != nil {
		return err
	}

	return nil
}

func (c *nptCompiler) rejectZeroIID(
	table uint8,
	field string,
	source netip.Prefix,
	delta int,
) error {
	// Reject a zero IID in the incoming or translated address.
	words := []uint16{0}
	if delta > 0 {
		words = append(words, uint16((wordModulus-delta)&wordModulus))
	}

	for _, word := range words {
		match := "ipv6," + field + "=" + wordMatch(source, iidWords, true, 0, word)
		if err := c.reject(table, invalidPriority, match); err != nil {
			return err
		}
	}

	return nil
}

func (c *nptCompiler) reject(table uint8, priority uint16, match string) error {
	if c.clearTable == 0 {
		clearTable, err := c.table()
		if err != nil {
			return err
		}

		c.clearTable = clearTable
		if err := c.rule(clearTable, 1, "", "clear_actions"); err != nil {
			return err
		}
	}

	return c.rule(table, priority, match, fmt.Sprintf("resubmit(,%d),exit", c.clearTable))
}

func (c *nptCompiler) arithmetic(stages [6]uint8, direction, delta int) error {
	base := fmt.Sprintf("reg3=%d", direction)
	setCarry := func(carry int) string {
		return fmt.Sprintf("load:%d->NXM_NX_REG1[],resubmit(,%d)", carry, stages[1])
	}
	if err := c.rule(stages[0], 1, base, setCarry(0)); err != nil {
		return err
	}

	// Include an initial carry when the sum is at least 65535 to encode zero as 0x0000.
	for low := wordModulus - delta; low < wordRange; {
		size := min(bits.TrailingZeros(uint(low)), bits.Len(uint(wordRange-low))-1)
		mask := wordModulus &^ ((1 << size) - 1)
		if err := c.rule(
			stages[0],
			nptPriority,
			fmt.Sprintf("%s,reg0=%#x/%#x", base, low, mask),
			setCarry(1),
		); err != nil {
			return err
		}

		low += 1 << size
	}

	for digit := range 4 {
		shift := nibbleWidth * digit

		for carry := range 2 {
			for val := range 16 {
				result := val + ((delta >> shift) & nibbleMask) + carry
				match := fmt.Sprintf(
					"%s,reg1=%d,reg0=%#x/%#x",
					base,
					carry,
					val<<shift,
					nibbleMask<<shift,
				)
				action := fmt.Sprintf(
					"load:%d->NXM_NX_REG0[%d..%d],load:%d->NXM_NX_REG1[],resubmit(,%d)",
					result&nibbleMask,
					shift,
					shift+nibbleWidth-1,
					result>>nibbleWidth,
					stages[digit+2],
				)
				if err := c.rule(stages[digit+1], nptPriority, match, action); err != nil {
					return err
				}
			}
		}
	}

	return nil
}
