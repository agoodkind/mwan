//go:build linux

package bpf

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/cilium/ebpf"
	"github.com/vishvananda/netlink"
)

// VerifyUnused inspects current policies and surviving managed attachments before edge release.
func (translator *Translator) VerifyUnused(indices []int, address netip.Addr, policies []InterfacePolicy) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPTv6 edge policy inspection failed", "edge", address, "err", resultErr)
		}
	}()
	translator.mu.Lock()
	defer translator.mu.Unlock()
	if translator.closed {
		return fmt.Errorf("NPTv6 translator is closed")
	}
	desired, err := preparePolicies(policies)
	if err != nil {
		return err
	}
	for _, index := range indices {
		expected, configured := desired[index]
		if err := translator.verifyEdgePolicy(index, address, expected, configured); err != nil {
			return err
		}
		if err := translator.verifyEdgeAttachments(index, configured); err != nil {
			return err
		}
	}
	return translator.verifySurvivingEdgeReferences(indices, address, desired)
}

func (translator *Translator) verifyEdgePolicy(index int, address netip.Addr, expected preparedPolicy, configured bool) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPTv6 edge map verification failed", "interface", index, "err", resultErr)
		}
	}()
	key, err := interfaceKey(index)
	if err != nil {
		return err
	}
	var policy nptPolicy
	err = translator.objects.Policies.Lookup(key, &policy)
	if err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
		return fmt.Errorf("read NPTv6 policy for interface %d: %w", index, err)
	}
	if errors.Is(err, ebpf.ErrKeyNotExist) {
		if configured {
			return fmt.Errorf("interface %d desired NPTv6 policy is absent", index)
		}
		return nil
	}
	if !configured || policy != expected.value {
		return fmt.Errorf("interface %d NPTv6 policy differs from desired policy", index)
	}
	if err := policyDoesNotUseEdge(policy, address); err != nil {
		return fmt.Errorf("interface %d: %w", index, err)
	}
	return nil
}

func (translator *Translator) verifyEdgeAttachments(index int, configured bool) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("NPTv6 edge attachment verification failed", "interface", index, "err", resultErr)
		}
	}()
	link, err := netlink.LinkByIndex(index)
	if errors.As(err, new(netlink.LinkNotFoundError)) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read NPTv6 edge interface %d: %w", index, err)
	}
	for _, direction := range []Direction{Ingress, Egress} {
		filters, err := netlink.FilterList(link, filterParent(direction))
		if err != nil {
			return fmt.Errorf("inspect NPTv6 edge attachments: %w", err)
		}
		for _, filter := range filters {
			if !ownedFilter(filter, direction) {
				continue
			}
			if !configured {
				return fmt.Errorf("interface %d retains an obsolete NPTv6 attachment", index)
			}
			program := translator.objects.NptIngress
			if direction == Egress {
				program = translator.objects.NptEgress
			}
			if err := readFilter(link, direction, program); err != nil {
				return fmt.Errorf("verify relevant NPTv6 attachment: %w", err)
			}
		}
	}
	return nil
}

func policyDoesNotUseEdge(policy nptPolicy, address netip.Addr) error {
	if policy.Count > uint32(len(policy.Pairs)) {
		return fmt.Errorf("NPTv6 policy has an invalid pair count")
	}
	for _, pair := range policy.Pairs[:policy.Count] {
		if pair.ExternalBits > 128 || pair.InternalBits > 128 || int(pair.SourceCount) > len(pair.SourceExceptions) || int(pair.DestinationCount) > len(pair.DestinationExceptions) {
			return fmt.Errorf("NPTv6 policy contains an invalid pair")
		}
		for _, exception := range pair.SourceExceptions[:pair.SourceCount] {
			if netip.AddrFrom16(exception) == address {
				return fmt.Errorf("NPTv6 source exception still uses edge %s", address)
			}
		}
		for _, exception := range pair.DestinationExceptions[:pair.DestinationCount] {
			if netip.AddrFrom16(exception) == address {
				return fmt.Errorf("NPTv6 destination exception still uses edge %s", address)
			}
		}
	}
	return nil
}
