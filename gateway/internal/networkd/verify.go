package networkd

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

// VerifyDir compares the complete render, including parent VLAN references, without changing files.
func VerifyDir(dir string, connections []interfaceintent.Connection, tables map[connectionid.ID]int, preserveStatic map[connectionid.ID]bool) error {
	rendered, err := renderAll(connections, tables, preserveStatic)
	if err != nil {
		return err
	}
	var failures []error
	for _, name := range slices.Sorted(maps.Keys(rendered)) {
		path := filepath.Join(dir, name)
		if err := verifyExpectedUnit(path, rendered[name].content); err != nil {
			failures = append(failures, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		slog.Error("networkd: reading unit directory failed", "dir", dir, "err", err)
		return errors.Join(append(failures, fmt.Errorf("read %s: %w", dir, err))...)
	}
	for _, entry := range entries {
		name := entry.Name()
		if _, expected := rendered[name]; expected || !entry.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, name)
		content, err := os.ReadFile(path)
		if err != nil {
			slog.Error("networkd: reading additional unit failed", "path", path, "err", err)
			failures = append(failures, fmt.Errorf("read %s: %w", path, err))
			continue
		}
		firstLine, _, _ := strings.Cut(string(content), "\n")
		if firstLine == Marker {
			failures = append(failures, fmt.Errorf("%s: stale generated unit", path))
		}
	}
	return errors.Join(failures...)
}

func verifyExpectedUnit(path string, expected string) error {
	info, err := os.Lstat(path)
	if err != nil {
		slog.Error("networkd: inspecting expected unit failed", "path", path, "err", err)
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: expected a regular unit file", path)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		slog.Error("networkd: reading expected unit failed", "path", path, "err", err)
		return fmt.Errorf("read %s: %w", path, err)
	}
	if !bytes.Equal(content, []byte(expected)) {
		return fmt.Errorf("%s: content differs from configured intent", path)
	}
	return nil
}
