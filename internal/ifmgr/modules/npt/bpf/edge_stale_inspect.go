//go:build linux

package bpf

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"slices"

	"github.com/cilium/ebpf"
	"github.com/vishvananda/netlink"
)

func (translator *Translator) verifySurvivingEdgeReferences(indices []int, address netip.Addr, desired map[int]preparedPolicy) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("Surviving NPTv6 edge inspection failed", "edge", address, "err", resultErr)
		}
	}()
	iterator := translator.objects.Policies.Iterate()
	var key uint32
	var policy nptPolicy
	for iterator.Next(&key, &policy) {
		if slices.Contains(indices, int(key)) {
			continue
		}
		if err := verifySurvivingEdgePolicy(int(key), policy, address, desired); err != nil {
			return err
		}
	}
	if err := iterator.Err(); err != nil {
		return fmt.Errorf("inspect surviving NPTv6 policy keys: %w", err)
	}
	links, err := netlink.LinkList()
	if err != nil {
		return fmt.Errorf("inspect surviving NPTv6 interfaces: %w", err)
	}
	for _, link := range links {
		for _, direction := range []Direction{Ingress, Egress} {
			filters, err := netlink.FilterList(link, filterParent(direction))
			if err != nil {
				return fmt.Errorf("inspect surviving NPTv6 attachments on interface %d: %w", link.Attrs().Index, err)
			}
			for _, filter := range filters {
				if !ownedFilter(filter, direction) {
					continue
				}
				attached, ok := filter.(*netlink.BpfFilter)
				if !ok {
					return fmt.Errorf("interface %d managed NPTv6 filter has no BPF program", link.Attrs().Index)
				}
				if err := translator.verifyAttachedEdgePolicy(link.Attrs().Index, attached.Id, address, desired); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func verifySurvivingEdgePolicy(index int, policy nptPolicy, address netip.Addr, desired map[int]preparedPolicy) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("Surviving NPTv6 policy references an obsolete edge", "interface", index, "edge", address, "err", resultErr)
		}
	}()
	if expected, configured := desired[index]; configured && policy == expected.value {
		return policyDoesNotUseEdge(policy, address)
	}
	if err := policyDoesNotUseEdge(policy, address); err != nil {
		return fmt.Errorf("interface %d surviving policy: %w", index, err)
	}
	for _, pair := range policy.Pairs[:policy.Count] {
		external := netip.PrefixFrom(netip.AddrFrom16(pair.External), int(pair.ExternalBits))
		if external.Contains(address) {
			return fmt.Errorf("interface %d obsolete NPTv6 prefix still uses edge %s", index, address)
		}
	}
	return nil
}

func (translator *Translator) verifyAttachedEdgePolicy(index, programID int, address netip.Addr, desired map[int]preparedPolicy) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("Attached NPTv6 policy inspection failed", "interface", index, "program", programID, "err", resultErr)
		}
	}()
	if programID <= 0 || uint64(programID) > math.MaxUint32 {
		return fmt.Errorf("interface %d managed NPTv6 attachment has no program identity", index)
	}
	program, err := ebpf.NewProgramFromID(ebpf.ProgramID(programID))
	if err != nil {
		return fmt.Errorf("open attached NPTv6 program on interface %d: %w", index, err)
	}
	defer program.Close()
	info, err := program.Info()
	if err != nil {
		return fmt.Errorf("inspect attached NPTv6 program on interface %d: %w", index, err)
	}
	mapIDs, available := info.MapIDs()
	if !available {
		return fmt.Errorf("interface %d attached NPTv6 program maps are unavailable", index)
	}
	expected, err := translator.objects.Policies.Info()
	if err != nil {
		return fmt.Errorf("inspect NPTv6 policy map schema: %w", err)
	}
	found := false
	for _, mapID := range mapIDs {
		matched, err := verifyAttachedMapEdge(mapID, index, address, desired, expected)
		if err != nil {
			return err
		}
		found = found || matched
	}
	if !found {
		return fmt.Errorf("interface %d attached NPTv6 policy map is unavailable", index)
	}
	return nil
}

func verifyAttachedMapEdge(mapID ebpf.MapID, index int, address netip.Addr, desired map[int]preparedPolicy, expected *ebpf.MapInfo) (matched bool, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("Attached NPTv6 map inspection failed", "interface", index, "map", mapID, "err", resultErr)
		}
	}()
	policyMap, err := ebpf.NewMapFromID(mapID)
	if err != nil {
		return false, fmt.Errorf("open attached NPTv6 map: %w", err)
	}
	defer policyMap.Close()
	info, err := policyMap.Info()
	if err != nil {
		return false, fmt.Errorf("inspect attached NPTv6 map: %w", err)
	}
	if info.Name != expected.Name {
		return false, nil
	}
	if info.Type != expected.Type || info.KeySize != expected.KeySize || info.ValueSize != expected.ValueSize {
		return true, fmt.Errorf("interface %d attached NPTv6 policy map schema differs", index)
	}
	key, err := interfaceKey(index)
	if err != nil {
		return true, err
	}
	var policy nptPolicy
	if err := policyMap.Lookup(key, &policy); errors.Is(err, ebpf.ErrKeyNotExist) {
		return true, nil
	} else if err != nil {
		return true, fmt.Errorf("read attached NPTv6 policy on interface %d: %w", index, err)
	}
	return true, verifySurvivingEdgePolicy(index, policy, address, desired)
}
