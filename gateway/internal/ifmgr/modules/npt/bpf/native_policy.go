//go:build linux

package bpf

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/cilium/ebpf"
	"github.com/vishvananda/netlink"
)

type nativePrograms struct {
	ingress uint32
	egress  uint32
}

// InspectNativePolicy permits only kernel-default queues and empty classifier hooks.
// An NPT owner must verify its own programs and maps through Translator.InspectNativePolicy.
func InspectNativePolicy(ctx context.Context, indices []int) error {
	return inspectNativeInterfaces(ctx, indices, nil)
}

// InspectNativePolicy requires the current translator's exact programs and applied
// map values. Previous pinned generations cannot authorize native translation.
func (translator *Translator) InspectNativePolicy(ctx context.Context, indices []int, policies []InterfacePolicy) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "native traffic-control policy inspection failed", "err", resultErr)
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
	programs := make(map[int]nativePrograms, len(desired))
	for _, index := range indices {
		policy, configured := desired[index]
		if !configured {
			continue
		}
		var actual nptPolicy
		if err := translator.objects.Policies.Lookup(policy.key, &actual); err != nil {
			return fmt.Errorf("inspect native NPTv6 map for interface %d: %w", index, err)
		}
		if actual != policy.value {
			return fmt.Errorf("interface %d native NPTv6 map differs from applied policy", index)
		}
		ingress, err := ReadNativeProgramID(translator.objects.NptIngress, translator.objects.Policies)
		if err != nil {
			return err
		}
		egress, err := ReadNativeProgramID(translator.objects.NptEgress, translator.objects.Policies)
		if err != nil {
			return err
		}
		programs[index] = nativePrograms{ingress: ingress, egress: egress}
	}
	return inspectNativeInterfaces(ctx, indices, programs)
}

// ReadNativeProgramID requires kernel proof that the current classifier references its exact policy map.
func ReadNativeProgramID(program *ebpf.Program, policies *ebpf.Map) (result uint32, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn("native NPT program identity read failed", "program_fd", program.FD(), "policy_map_fd", policies.FD(), "err", resultErr)
		}
	}()
	info, err := program.Info()
	if err != nil {
		return 0, fmt.Errorf("inspect current NPTv6 program: %w", err)
	}
	identity, available := info.ID()
	if !available || info.Type != ebpf.SchedCLS {
		return 0, fmt.Errorf("current NPTv6 program lacks a classifier identity")
	}
	mapInfo, err := policies.Info()
	if err != nil {
		return 0, fmt.Errorf("inspect current NPTv6 policy map: %w", err)
	}
	mapID, available := mapInfo.ID()
	if !available {
		return 0, fmt.Errorf("current NPTv6 policy map lacks an identity")
	}
	maps, available := info.MapIDs()
	if !available || len(maps) != 1 || maps[0] != mapID {
		return 0, fmt.Errorf("current NPTv6 program does not reference its exact policy map")
	}
	return uint32(identity), nil
}

func inspectNativeInterfaces(ctx context.Context, indices []int, programs map[int]nativePrograms) (resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.WarnContext(ctx, "native interface packet policy inspection failed", "interfaces", indices, "err", resultErr)
		}
	}()
	for _, index := range indices {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("inspect native interface packet policy: %w", err)
		}
		link, err := netlink.LinkByIndex(index)
		if err != nil {
			return fmt.Errorf("inspect native traffic-control interface %d: %w", index, err)
		}
		if err := inspectNativeQueues(ctx, link); err != nil {
			return fmt.Errorf("interface %s: %w", link.Attrs().Name, err)
		}
		for _, direction := range []Direction{Ingress, Egress} {
			identity := programs[index].ingress
			if direction == Egress {
				identity = programs[index].egress
			}
			if err := inspectNativeFilters(ctx, index, direction, identity); err != nil {
				return fmt.Errorf("interface %s %s: %w", link.Attrs().Name, direction, err)
			}
		}
	}
	return nil
}
