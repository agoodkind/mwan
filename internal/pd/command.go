package pd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
)

func (s *DefaultSource) command(
	ctx context.Context,
	log *slog.Logger,
	name string,
	args []string,
) ([]byte, error) {
	runCommand := s.runCommand
	if runCommand == nil {
		runCommand = runCommandOutput
	}
	return runCommand(ctx, log, name, args)
}

func runCommandOutput(
	ctx context.Context,
	log *slog.Logger,
	name string,
	args []string,
) ([]byte, error) {
	if log == nil {
		log = slog.Default()
	}
	log.DebugContext(ctx, "pd: command start", "command", name, "args", args)
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.Output()
	if err != nil {
		stderr := ""
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			stderr = string(exitErr.Stderr)
		}
		log.WarnContext(
			ctx,
			"pd: command failed",
			"command",
			name,
			"args",
			args,
			"err",
			err,
			"stderr",
			stderr,
		)
		return nil, fmt.Errorf("run %s: %w (stderr=%q)", name, err, stderr)
	}
	log.DebugContext(ctx, "pd: command ok", "command", name, "args", args, "out_bytes", len(output))
	return output, nil
}
