package deployoperation

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"goodkind.io/mwan/internal/ops"
)

// Paths restricts identity reads to the configured executable and recovery documents.
type Paths struct {
	Executable string `json:"executable"`
	Network    string `json:"network"`
	Runtime    string `json:"runtime"`
}

// ReadIdentity reads only machine identity and hashes through the existing guest transport.
func ReadIdentity(ctx context.Context, operations *ops.RealOps, vmid string, paths Paths) (Identity, error) {
	for _, path := range []string{paths.Executable, paths.Network, paths.Runtime} {
		if !filepath.IsAbs(path) {
			return Identity{}, fmt.Errorf("deploy identity requires absolute executable, network and runtime paths")
		}
	}
	machine, err := guestText(ctx, operations, vmid, "cat", "/etc/machine-id")
	if err != nil {
		return Identity{}, err
	}
	boot, err := guestText(ctx, operations, vmid, "cat", "/proc/sys/kernel/random/boot_id")
	if err != nil {
		return Identity{}, err
	}
	executable, err := guestDigest(ctx, operations, vmid, paths.Executable)
	if err != nil {
		return Identity{}, err
	}
	network, err := guestDigest(ctx, operations, vmid, paths.Network)
	if err != nil {
		return Identity{}, err
	}
	runtime, err := guestDigest(ctx, operations, vmid, paths.Runtime)
	if err != nil {
		return Identity{}, err
	}
	identity := Identity{
		MachineID: machine, BootID: boot, ExecutableSHA256: executable,
		NetworkSHA256: network, RuntimeSHA256: runtime,
	}
	if err := identity.validate(); err != nil {
		slog.WarnContext(ctx, "guest deployment identity validation failed")
		return Identity{}, fmt.Errorf("validate guest deploy identity: %w", err)
	}
	return identity, nil
}

func guestDigest(ctx context.Context, operations *ops.RealOps, vmid, path string) (string, error) {
	output, err := guestText(ctx, operations, vmid, "sha256sum", "--", path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || len(fields[0]) != 64 || fields[1] != path {
		return "", fmt.Errorf("deploy identity digest response is malformed")
	}
	return fields[0], nil
}

func guestText(ctx context.Context, operations *ops.RealOps, vmid string, command string, arguments ...string) (string, error) {
	response, err := operations.GuestExec(ctx, vmid, append([]string{command}, arguments...)...)
	if err != nil {
		slog.WarnContext(ctx, "guest deployment identity read failed")
		return "", fmt.Errorf("read deploy identity with %s: %w", command, err)
	}
	if response.ExitCode != 0 {
		return "", fmt.Errorf("deploy identity %s exited %d", command, response.ExitCode)
	}
	result := strings.TrimSpace(response.Stdout)
	if result == "" {
		return "", fmt.Errorf("deploy identity %s returned no data", command)
	}
	return result, nil
}
