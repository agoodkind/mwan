//go:build linux

package bpf

import (
	"fmt"
	"log/slog"

	"github.com/cilium/ebpf"
)

// ReadNativeProgramID requires kernel proof that the current classifier references its exact policy map.
func ReadNativeProgramID(
	program *ebpf.Program,
	policies *ebpf.Map,
) (result uint32, resultErr error) {
	defer func() {
		if resultErr != nil {
			slog.Warn(
				"native NPT program identity read failed",
				"program_fd",
				program.FD(),
				"policy_map_fd",
				policies.FD(),
				"err",
				resultErr,
			)
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
