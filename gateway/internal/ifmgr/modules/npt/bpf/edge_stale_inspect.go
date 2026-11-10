//go:build linux

package bpf

import (
	"errors"
	"fmt"
	"log/slog"
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

// Only the current programs and one previous pinned generation have known
// policy maps. An unmatched owned filter must block edge release until
// Reconcile removes the filter.
func (translator *Translator) policiesForProgram(programID int) (policies *ebpf.Map, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("Attached NPTv6 program has no known policy map", "program", programID, "err", resultErr)
		}
	}()
	generations := []*nptObjects{&translator.objects}
	if translator.previous != nil {
		generations = append(generations, translator.previous)
	}
	for _, generation := range generations {
		for _, program := range []*ebpf.Program{generation.NptIngress, generation.NptEgress} {
			info, err := program.Info()
			if err != nil {
				return nil, fmt.Errorf("inspect known NPTv6 program: %w", err)
			}
			knownID, available := info.ID()
			if !available {
				return nil, fmt.Errorf("kernel did not report a known NPTv6 program identity")
			}
			if int(knownID) == programID {
				return generation.Policies, nil
			}
		}
	}
	return nil, fmt.Errorf("owned NPTv6 filter program %d matches no program this translator or its pinned predecessor loaded", programID)
}

func (translator *Translator) verifyAttachedEdgePolicy(index, programID int, address netip.Addr, desired map[int]preparedPolicy) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("Attached NPTv6 policy inspection failed", "interface", index, "program", programID, "err", resultErr)
		}
	}()
	policies, err := translator.policiesForProgram(programID)
	if err != nil {
		return fmt.Errorf("interface %d: %w", index, err)
	}
	key, err := interfaceKey(index)
	if err != nil {
		return err
	}
	var policy nptPolicy
	if err := policies.Lookup(key, &policy); errors.Is(err, ebpf.ErrKeyNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("read attached NPTv6 policy on interface %d: %w", index, err)
	}
	return verifySurvivingEdgePolicy(index, policy, address, desired)
}
