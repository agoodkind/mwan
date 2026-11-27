package ovs

// OpenFlow compilation reserves tables that foreign entries or configured
// rules already use or reference. A flow calls its translation helper before
// its configured actions. A group bucket uses a wrapper table in this order.
//
//   bucket resubmit -> translate prefix -> run original bucket actions

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"

	"antrea.io/libOpenflow/openflow15"
	"antrea.io/libOpenflow/util"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

var errCurrentTable = errors.New(
	"a connection bucket requires an explicit table number for resubmit",
)

func reserveInstructions(compiler *nptCompiler, data []byte, bucket bool) (bucketProgram, error) {
	var refs [256]byte
	ordered, err := inspectActions(data, bucket, &refs)
	if err != nil {
		return bucketProgram{}, err
	}

	for table, seen := range refs {
		if seen != 0 {
			compiler.used[table] = true
		}
	}

	return ordered, nil
}

func prependCall(flow *openflow15.FlowMod, table uint8) error {
	call, err := marshalText(bucketExpression, fmt.Sprintf("resubmit(,%d)", table), nil)
	if err != nil {
		return err
	}

	if int(flow.Len())+len(call)+8 > maxFlowSize {
		return errFlowSize
	}

	for index, instruction := range flow.Instructions {
		data, err := instruction.MarshalBinary()
		if err != nil {
			slog.Error("OpenFlow instruction encoding failed", "err", err)

			return fmt.Errorf("encode connection instructions: %w", err)
		}

		if binary.BigEndian.Uint16(data) == openflow15.InstrType_APPLY_ACTIONS {
			flow.Instructions[index], err = applyWire(append(call, data[instructionSize:]...))

			return err
		}
	}

	instruction, err := applyWire(call)
	if err != nil {
		return err
	}

	flow.Instructions = slices.Insert(flow.Instructions, 0, openflow15.Instruction(instruction))

	return nil
}

func prepareNPT(
	configured []*openflow15.FlowMod,
	installed installedEntries,
) (*nptCompiler, error) {
	compiler := new(nptCompiler)
	compiler.groups = maps.Clone(installed.dependencies)
	if compiler.groups == nil {
		compiler.groups = make(map[uint32][]bucketProgram)
	}

	for table, reserved := range installed.references {
		compiler.used[table] = reserved != 0
	}

	for identity, entry := range installed.flows {
		if entry.cookie&mwanFlowMask == mwanFlowCookie {
			continue
		}

		compiler.used[identity.table] = true
		if _, err := reserveInstructions(compiler, []byte(entry.instructions), false); err != nil {
			return nil, err
		}
	}

	for _, flow := range configured[1:] {
		compiler.used[flow.TableId] = true
		encoded, err := actionBytes(flow.Instructions)
		if err != nil {
			return nil, err
		}

		if _, err = reserveInstructions(compiler, encoded, false); err != nil {
			return nil, err
		}
	}

	return compiler, nil
}

func compileNPT(
	cfg *ovsconfig.OpenFlowConfig,
	installed installedEntries,
	configured []*openflow15.FlowMod,
	flows []*openflow15.FlowMod,
	groups []*openflow15.GroupMod,
) ([]*openflow15.FlowMod, error) {
	referenced := referencedMappings(cfg)
	if len(referenced) == 0 {
		return flows, nil
	}

	compiler, err := prepareNPT(configured, installed)
	if err != nil {
		return nil, err
	}

	ordered, err := compiler.bucketActions(groups)
	if err != nil {
		return nil, err
	}

	entries := make(map[string]uint8)

	for _, name := range slices.Sorted(maps.Keys(referenced)) {
		entry, err := compiler.mapping(cfg.Mappings[name])
		if err != nil {
			slog.Error("Connection translation compilation failed", "connection", name, "err", err)

			return nil, fmt.Errorf("compile connection %q translation: %w", name, err)
		}

		entries[name] = entry
	}

	byCookie := make(map[uint64]uint8)

	for _, rule := range cfg.Flows {
		if table, ok := entries[rule.Connection]; ok {
			byCookie[rule.Cookie()] = table
		}
	}

	for _, flow := range flows[1:] {
		if table, ok := byCookie[flow.Cookie]; ok {
			if err := prependCall(flow, table); err != nil {
				return nil, err
			}
		}
	}

	if err := compiler.attachBuckets(cfg, groups, ordered, entries); err != nil {
		return nil, err
	}

	return append(flows, compiler.helpers...), nil
}

type bucketKey struct{ group, bucket int }

func (compiler *nptCompiler) bucketActions(
	groups []*openflow15.GroupMod,
) (map[bucketKey]bucketProgram, error) {
	ordered := make(map[bucketKey]bucketProgram)

	for groupIndex, group := range groups {
		for bucketIndex, bucket := range group.Buckets {
			data, err := actionBytes(bucket.Actions)
			if err != nil {
				return nil, err
			}

			actions, err := reserveInstructions(compiler, data, true)
			if err != nil {
				return nil, err
			}

			ordered[bucketKey{groupIndex, bucketIndex}] = actions
			compiler.groups[group.GroupId] = append(compiler.groups[group.GroupId], actions)
		}
	}

	return ordered, nil
}

func referencedMappings(cfg *ovsconfig.OpenFlowConfig) map[string]bool {
	referenced := make(map[string]bool)

	for _, rule := range cfg.Flows {
		if _, exists := cfg.Mappings[rule.Connection]; exists {
			referenced[rule.Connection] = true
		}
	}

	for _, group := range cfg.Groups {
		for _, bucket := range group.Buckets {
			if _, exists := cfg.Mappings[bucket.Connection]; exists {
				referenced[bucket.Connection] = true
			}
		}
	}

	return referenced
}

func (compiler *nptCompiler) attachBuckets(
	cfg *ovsconfig.OpenFlowConfig,
	groups []*openflow15.GroupMod,
	ordered map[bucketKey]bucketProgram,
	entries map[string]uint8,
) error {
	for groupIndex, group := range groups {
		for bucketIndex := range group.Buckets {
			bucket := &group.Buckets[bucketIndex]
			table, ok := entries[cfg.Groups[groupIndex].Buckets[bucketIndex].Connection]
			if !ok {
				continue
			}

			wrapper, err := compiler.table()
			if err != nil {
				return err
			}

			flow := openflow15.NewFlowMod()
			flow.TableId = wrapper
			flow.Cookie = mwanFlowCookie
			program := ordered[bucketKey{groupIndex, bucketIndex}]
			if program.needsCaller(compiler.groups, make(map[uint32]bool)) {
				slog.Error(
					"Ambiguous bucket resubmit",
					"group",
					group.GroupId,
					"bucket",
					bucket.BucketId,
					"err",
					errCurrentTable,
				)

				return fmt.Errorf(
					"group %d bucket %d: %w",
					group.GroupId,
					bucket.BucketId,
					errCurrentTable,
				)
			}

			actions := program.actions
			if len(actions) != 0 {
				apply, err := applyWire(actions)
				if err != nil {
					return err
				}

				flow.AddInstruction(apply)
			}

			if err := prependCall(flow, table); err != nil {
				return err
			}

			compiler.helpers = append(compiler.helpers, flow)
			call, err := marshalText(bucketExpression, fmt.Sprintf("resubmit(,%d)", wrapper), nil)
			if err != nil {
				return err
			}

			bucket.Actions = nil
			bucket.ActionArrayLen = 0
			bucket.AddAction(wireAction{
				Buffer: util.NewBuffer(call),
				header: openflow15.ActionHeader{
					Type:   binary.BigEndian.Uint16(call),
					Length: binary.BigEndian.Uint16(call[2:]),
				},
			})
		}
	}

	return nil
}
