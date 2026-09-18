package ops

import (
	"context"
	"strconv"
	"time"
)

// GuestExecViaQm runs `qm guest exec` for the given VM with an agent-side
// timeout and returns the raw JSON response. The deploy gate uses it to read
// the guest boot_id from the hypervisor without the watchdog's richer
// channels; the integer vmid keeps the argv free of caller-shaped strings.
// Linux-only because its only caller, the deploy gate, runs on the Proxmox
// host.
func GuestExecViaQm(
	ctx context.Context,
	timeout time.Duration,
	agentTimeout time.Duration,
	vmid int,
	command ...string,
) ([]byte, error) {
	args := append([]string{
		"guest", "exec", strconv.Itoa(vmid),
		"--timeout", strconv.Itoa(int(agentTimeout.Seconds())),
		"--",
	}, command...)
	return runQm(ctx, timeout, args...)
}
