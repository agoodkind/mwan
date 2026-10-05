package main

import (
	"context"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/mwan/internal/installspec"
)

const (
	containerDetectProgram = "systemd-detect-virt"
	containerDetectFlag    = "--container"
	// namespacedSysctlPrefix marks the keys a container can write. The keys
	// under net. belong to the container's network namespace.
	namespacedSysctlPrefix = "net."
	commentPrefix          = "#"
	blockSeparator         = "\n\n"
)

// detectContainer reports whether systemd-detect-virt finds a container. A
// non-zero exit means no container, and a failure to start the program fails
// the install.
func detectContainer(ctx context.Context) (bool, error) {
	err := exec.CommandContext(ctx, containerDetectProgram, containerDetectFlag).Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return false, installFailed("detect a container with", containerDetectProgram, err)
	}
	inContainer := err == nil
	slog.InfoContext(ctx, "install: container detection", "container", inContainer)
	return inContainer, nil
}

func isSysctlFile(file installspec.File) bool {
	return filepath.Dir(file.Dest) == installspec.SysctlDir
}

func roleInstallsSysctl(spec installspec.Spec) bool {
	return slices.ContainsFunc(spec.Files, isSysctlFile)
}

// namespacedSysctlSettings keeps the net. settings of a sysctl file, each with
// the comments above it. It returns nil when no setting remains.
func namespacedSysctlSettings(content []byte) []byte {
	var keptBlocks []string
	for block := range strings.SplitSeq(strings.TrimSpace(string(content)), blockSeparator) {
		var comments []string
		var settings []string
		for line := range strings.SplitSeq(block, "\n") {
			trimmed := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(trimmed, commentPrefix):
				comments = append(comments, trimmed)
			case strings.HasPrefix(trimmed, namespacedSysctlPrefix):
				settings = append(settings, trimmed)
			}
		}
		if len(settings) == 0 {
			continue
		}
		keptBlocks = append(keptBlocks, strings.Join(append(comments, settings...), "\n"))
	}
	if len(keptBlocks) == 0 {
		return nil
	}
	return []byte(strings.Join(keptBlocks, blockSeparator) + "\n")
}
