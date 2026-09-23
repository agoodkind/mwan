//go:build linux

package bpf

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"

	"github.com/cilium/ebpf"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

type packetHeader string

const (
	headerEthernet packetHeader = "ether"
	headerLoopback packetHeader = "loopback"
	headerNone     packetHeader = "none"
	headerTunnel   packetHeader = "tunnel"
	headerTunnel6  packetHeader = "tunnel6"
	headerSIT      packetHeader = "sit"
	headerIPIP     packetHeader = "ipip"
	headerPPP      packetHeader = "ppp"
	headerRawIP    packetHeader = "rawip"
)

const (
	filterPriority = 45010
	filterHandle   = 0x4e50
)

// Translator manages kernel programs, policy maps, and traffic-control attachments.
type Translator struct {
	mu      sync.Mutex
	objects nptObjects
	closed  bool
}

// New loads the embedded programs and creates their shared policy map.
func New() (*Translator, error) {
	translator := new(Translator)
	if err := loadNptObjects(&translator.objects, nil); err != nil {
		slog.Error("NPTv6 eBPF programs could not be loaded", "err", err)
		return nil, fmt.Errorf("load NPTv6 programs: %w", err)
	}
	return translator, nil
}

func policyValue(policy InterfacePolicy, link netlink.Link) (nptPolicy, error) {
	var value nptPolicy
	pairCount := len(policy.Pairs)
	if pairCount > MaxPairs {
		return value, fmt.Errorf("interface %d exceeds %d prefix pairs", policy.IfIndex, MaxPairs)
	}
	value.Count = uint32(pairCount)
	if policy.Internal {
		value.Internal = 1
	}
	switch packetHeader(link.Attrs().EncapType) {
	case headerEthernet, headerLoopback:
		value.Ethernet = 1
	case headerNone, headerTunnel, headerTunnel6, headerSIT, headerIPIP, headerPPP, headerRawIP:
	default:
		return value, fmt.Errorf("interface %d has unsupported packet header %q", policy.IfIndex, link.Attrs().EncapType)
	}
	identities := make(map[uint16]struct{}, len(policy.Pairs))
	for index, pair := range policy.Pairs {
		if _, exists := identities[pair.ID]; exists {
			return value, fmt.Errorf("interface %d repeats pair %d", policy.IfIndex, pair.ID)
		}
		identities[pair.ID] = struct{}{}
		mapped, err := pairValue(pair)
		if err != nil {
			return value, err
		}

		value.Pairs[index] = mapped
		value.IngressOrder[index] = uint8(index)
		value.EgressOrder[index] = uint8(index)
	}
	sort.SliceStable(value.IngressOrder[:len(policy.Pairs)], func(i, j int) bool {
		return value.Pairs[value.IngressOrder[i]].ExternalBits > value.Pairs[value.IngressOrder[j]].ExternalBits
	})
	sort.SliceStable(value.EgressOrder[:len(policy.Pairs)], func(i, j int) bool {
		return value.Pairs[value.EgressOrder[i]].InternalBits > value.Pairs[value.EgressOrder[j]].InternalBits
	})
	return value, nil
}

func pairValue(pair PrefixPair) (nptPair, error) {
	var mapped nptPair
	if err := ValidatePair(pair); err != nil {
		return mapped, err
	}
	internalBits, externalBits := pair.Internal.Bits(), pair.External.Bits()
	if internalBits < 0 || internalBits > 64 || externalBits < 0 || externalBits > 64 {
		return mapped, fmt.Errorf("pair %d has an unencodable prefix length", pair.ID)
	}
	mapped.Internal = pair.Internal.Addr().As16()
	mapped.External = pair.External.Addr().As16()
	mapped.ID = pair.ID
	mapped.InternalBits = uint8(internalBits)
	mapped.ExternalBits = uint8(externalBits)
	mapped.ForwardAdjustment = adjustment(pair.Internal, pair.External)
	mapped.ReverseAdjustment = adjustment(pair.External, pair.Internal)
	for bit := range max(internalBits, externalBits) {
		mapped.Mask[bit/8] |= 1 << (7 - bit%8)
	}
	for i, address := range pair.SourceExceptions {
		mapped.SourceExceptions[i] = address.As16()
		mapped.SourceCount++
	}
	for i, address := range pair.DestinationExceptions {
		mapped.DestinationExceptions[i] = address.As16()
		mapped.DestinationCount++
	}
	return mapped, nil
}

func filterParent(direction Direction) uint32 {
	if direction == Ingress {
		return netlink.HANDLE_MIN_INGRESS
	}
	return netlink.HANDLE_MIN_EGRESS
}

func filterName(direction Direction) string { return "mwan-npt-" + string(direction) }

func ownedFilter(filter netlink.Filter, direction Direction) bool {
	bpfFilter, ok := filter.(*netlink.BpfFilter)
	return ok && bpfFilter.Name == filterName(direction) && bpfFilter.Handle == filterHandle && bpfFilter.Priority == filterPriority
}

func installFilter(link netlink.Link, direction Direction, program *ebpf.Program) error {
	filters, err := netlink.FilterList(link, filterParent(direction))
	if err != nil {
		slog.Error("NPTv6 filters could not be read", "interface", link.Attrs().Index, "direction", direction, "err", err)
		return fmt.Errorf("list NPTv6 filters: %w", err)
	}
	for _, filter := range filters {
		if filter.Attrs().Priority == filterPriority && !ownedFilter(filter, direction) {
			return fmt.Errorf("interface %d %s priority %d is already used", link.Attrs().Index, direction, filterPriority)
		}
	}
	if err := netlink.FilterReplace(&netlink.BpfFilter{
		FilterAttrs: netlink.FilterAttrs{LinkIndex: link.Attrs().Index, Parent: filterParent(direction), Handle: filterHandle, Priority: filterPriority, Protocol: unix.ETH_P_ALL},
		Fd:          program.FD(), Name: filterName(direction), DirectAction: true,
	}); err != nil {
		slog.Error("NPTv6 filter replacement failed", "interface", link.Attrs().Index, "direction", direction, "err", err)
		return fmt.Errorf("replace NPTv6 filter: %w", err)
	}
	return nil
}

func readFilter(link netlink.Link, direction Direction, program *ebpf.Program) error {
	info, err := program.Info()
	if err != nil {
		slog.Error("NPTv6 program identity could not be read", "err", err)
		return fmt.Errorf("read NPTv6 program identity: %w", err)
	}
	expectedID, ok := info.ID()
	if !ok {
		return fmt.Errorf("kernel did not report NPTv6 program identity")
	}
	filters, err := netlink.FilterList(link, filterParent(direction))
	if err != nil {
		slog.Error("NPTv6 filters could not be read", "interface", link.Attrs().Index, "direction", direction, "err", err)
		return fmt.Errorf("list NPTv6 filters: %w", err)
	}
	for _, filter := range filters {
		actual, ok := filter.(*netlink.BpfFilter)
		if ok && ownedFilter(actual, direction) {
			if actual.Id != int(expectedID) || !actual.DirectAction || actual.Protocol != unix.ETH_P_ALL {
				return fmt.Errorf("interface %d %s NPTv6 program differs from desired program", link.Attrs().Index, direction)
			}
			return nil
		}
	}
	return fmt.Errorf("interface %d %s NPTv6 attachment is absent", link.Attrs().Index, direction)
}

func ensureClsact(link netlink.Link) error {
	qdiscs, err := netlink.QdiscList(link)
	if err != nil {
		slog.Error("NPTv6 queue disciplines could not be read", "interface", link.Attrs().Index, "err", err)
		return fmt.Errorf("list queue disciplines: %w", err)
	}
	for _, qdisc := range qdiscs {
		if qdisc.Type() == "clsact" {
			return nil
		}
	}
	if err := netlink.QdiscAdd(&netlink.Clsact{QdiscAttrs: netlink.QdiscAttrs{LinkIndex: link.Attrs().Index, Handle: netlink.MakeHandle(0xffff, 0), Parent: netlink.HANDLE_CLSACT}}); err != nil {
		slog.Error("NPTv6 queue discipline could not be added", "interface", link.Attrs().Index, "err", err)
		return fmt.Errorf("add clsact queue discipline: %w", err)
	}
	return nil
}

func removeFilters(link netlink.Link) error {
	var failures []error
	for _, direction := range []Direction{Ingress, Egress} {
		filters, err := netlink.FilterList(link, filterParent(direction))
		if err != nil {
			slog.Error("NPTv6 filter removal could not list filters", "interface", link.Attrs().Index, "direction", direction, "err", err)
			failures = append(failures, err)
			continue
		}
		for _, filter := range filters {
			if ownedFilter(filter, direction) {
				if err := netlink.FilterDel(filter); err != nil {
					slog.Error("NPTv6 filter could not be removed", "interface", link.Attrs().Index, "direction", direction, "err", err)
					failures = append(failures, err)
				}
			}
		}
	}
	return errors.Join(failures...)
}

type preparedPolicy struct {
	link  netlink.Link
	key   uint32
	value nptPolicy
}

func interfaceKey(index int) (uint32, error) {
	if index < 1 || index > math.MaxInt32 {
		return 0, fmt.Errorf("interface index %d is outside the Linux interface index range", index)
	}
	return uint32(index), nil
}

func preparePolicies(policies []InterfacePolicy) (map[int]preparedPolicy, error) {
	desired := make(map[int]preparedPolicy, len(policies))
	seen := make(map[int]struct{}, len(policies))
	for _, policy := range policies {
		if _, duplicate := seen[policy.IfIndex]; duplicate {
			return nil, fmt.Errorf("duplicate interface %d", policy.IfIndex)
		}
		seen[policy.IfIndex] = struct{}{}
		key, err := interfaceKey(policy.IfIndex)
		if err != nil {
			return nil, err
		}
		link, err := netlink.LinkByIndex(policy.IfIndex)
		if err != nil {
			slog.Error("NPTv6 reconciliation could not read interface", "interface", policy.IfIndex, "err", err)
			return nil, fmt.Errorf("read interface %d: %w", policy.IfIndex, err)
		}
		value, err := policyValue(policy, link)
		if err != nil {
			return nil, err
		}
		if len(policy.Pairs) != 0 {
			desired[policy.IfIndex] = preparedPolicy{link: link, key: key, value: value}
		}
	}
	return desired, nil
}

func (translator *Translator) reconcileInterface(policy preparedPolicy) ([]AttachmentState, error) {
	updateErr := ensureClsact(policy.link)
	if updateErr == nil {
		updateErr = translator.objects.Policies.Update(policy.key, policy.value, ebpf.UpdateAny)
	}
	var states []AttachmentState
	var failures []error
	for _, direction := range []Direction{Ingress, Egress} {
		program := translator.objects.NptIngress
		if direction == Egress {
			program = translator.objects.NptEgress
		}
		state := AttachmentState{IfIndex: policy.link.Attrs().Index, Direction: direction, Ready: false, Reason: ""}
		err := updateErr
		if err == nil {
			err = installFilter(policy.link, direction, program)
		}
		if err == nil {
			err = readFilter(policy.link, direction, program)
		}
		if err == nil {
			var actual nptPolicy
			err = translator.objects.Policies.Lookup(policy.key, &actual)
			if err == nil && actual != policy.value {
				err = fmt.Errorf("interface %d policy map differs from desired policy", state.IfIndex)
			}
		}
		if err != nil {
			state.Reason = err.Error()
			slog.Error("NPTv6 attachment did not match desired state", "interface", state.IfIndex, "direction", direction, "err", err)
			failures = append(failures, fmt.Errorf("interface %d %s: %w", state.IfIndex, direction, err))
		} else {
			state.Ready = true
		}
		states = append(states, state)
	}
	return states, errors.Join(failures...)
}

func (translator *Translator) removeStalePolicies(desired map[int]preparedPolicy) error {
	allLinks, err := netlink.LinkList()
	if err != nil {
		slog.Error("NPTv6 stale policy cleanup could not list interfaces", "err", err)
		return fmt.Errorf("list interfaces for stale policy cleanup: %w", err)
	}
	var failures []error
	for _, link := range allLinks {
		if _, exists := desired[link.Attrs().Index]; exists {
			continue
		}
		if err := removeFilters(link); err != nil {
			slog.Error("NPTv6 reconciliation could not remove stale attachments", "interface", link.Attrs().Index, "err", err)
			failures = append(failures, fmt.Errorf("remove interface %d NPTv6: %w", link.Attrs().Index, err))
		}
		key, err := interfaceKey(link.Attrs().Index)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err := translator.objects.Policies.Delete(key); err != nil && !errors.Is(err, ebpf.ErrKeyNotExist) {
			slog.Error("NPTv6 reconciliation could not remove stale policy", "interface", link.Attrs().Index, "err", err)
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Reconcile applies and verifies every active policy before removing stale attachments.
func (translator *Translator) Reconcile(policies []InterfacePolicy) ([]AttachmentState, error) {
	translator.mu.Lock()
	defer translator.mu.Unlock()
	if translator.closed {
		return nil, fmt.Errorf("NPTv6 translator is closed")
	}
	desired, err := preparePolicies(policies)
	if err != nil {
		return nil, err
	}
	var states []AttachmentState
	var failures []error
	for _, policy := range policies {
		prepared, active := desired[policy.IfIndex]
		if !active {
			continue
		}
		attachments, err := translator.reconcileInterface(prepared)
		states = append(states, attachments...)
		if err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) != 0 {
		err := errors.Join(failures...)
		slog.Error("NPTv6 reconciliation retained stale attachments after active policies failed", "failed_interfaces", len(failures), "err", err)
		return states, err
	}
	return states, translator.removeStalePolicies(desired)
}

// Close releases userspace descriptors. Classic tc filters retain the kernel programs and maps.
func (translator *Translator) Close() error {
	translator.mu.Lock()
	defer translator.mu.Unlock()
	if translator.closed {
		return nil
	}
	translator.closed = true
	return translator.objects.Close()
}
