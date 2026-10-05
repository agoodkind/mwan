package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// execGuest runs command inside the container through `pct exec`, which
// prints the command's raw stdout and stderr and exits with the command's
// status. waitTimeout kills the process; agentTimeout is unused because
// `pct exec` has no agent-side wait.
func (lxcGuest) execGuest(
	ctx context.Context,
	log *slog.Logger,
	vmid string,
	waitTimeout, _ time.Duration,
	command []string,
) (GuestCommandResult, error) {
	args := append([]string{"exec", vmid, "--"}, command...)
	cctx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	log.DebugContext(ctx, "ops: pct exec", "args", args, "timeout", waitTimeout)
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(cctx, pctBinary, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return lxcCommandResult(0, &stdout, &stderr), nil
	}
	if cctx.Err() != nil {
		log.WarnContext(ctx, "pct exec timed out",
			"vmid", vmid, "timeout", waitTimeout, "err", err)
		return failedGuestCommand(), fmt.Errorf(
			"guest command %q did not exit within %s",
			strings.Join(command, " "), waitTimeout)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
		return lxcCommandResult(exitErr.ExitCode(), &stdout, &stderr), nil
	}
	log.WarnContext(ctx, "pct exec failed",
		"vmid", vmid, "err", err, "stderr", strings.TrimSpace(stderr.String()))
	return failedGuestCommand(), fmt.Errorf("pct %s: %w", strings.Join(args, " "), err)
}

func lxcCommandResult(exitCode int, stdout, stderr *bytes.Buffer) GuestCommandResult {
	return GuestCommandResult{
		GuestExecResult: GuestExecResult{ExitCode: exitCode, Stdout: stdout.String()},
		Stderr:          stderr.String(),
	}
}
