package ops

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

func runHypervisor(
	ctx context.Context,
	binary string,
	timeout time.Duration,
	args ...string,
) ([]byte, error) {
	slog.DebugContext(ctx, "ops: runHypervisor", "binary", binary, "args", args, "timeout", timeout)
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := exec.CommandContext(cctx, binary, args...).CombinedOutput()
	if err != nil {
		// The caller decides whether a command failure requires an Error log.
		slog.WarnContext(
			ctx,
			"ops: hypervisor command failed",
			"binary",
			binary,
			"args",
			args,
			"err",
			err,
			"output",
			strings.TrimSpace(string(out)),
		)
		// The output is returned alongside the error because callers read the
		// combined output to decide what failed.
		return out, fmt.Errorf("%s %s: %w", binary, strings.Join(args, " "), err)
	}

	return out, nil
}

func runQm(
	ctx context.Context,
	timeout time.Duration,
	args ...string,
) ([]byte, error) {
	return runHypervisor(ctx, qmBinary, timeout, args...)
}
