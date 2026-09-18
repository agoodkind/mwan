package pd

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/google/renameio/v2"
)

func (s *DefaultSource) prefixFromStateFile(
	ctx context.Context,
	iface string,
) (netip.Prefix, bool, error) {
	log := s.log
	if log == nil {
		log = slog.Default()
	}
	path, err := s.stateFilePath(iface)
	if err != nil {
		log.WarnContext(ctx, "pd: state file path invalid", "iface", iface, "err", err)
		return netip.Prefix{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return netip.Prefix{}, false, nil
		}
		log.WarnContext(ctx, "pd: read state file failed", "iface", iface, "path", path, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("read state file %s: %w", path, err)
	}

	prefixes := parsePrefixLines(data)
	if len(prefixes) == 0 {
		return netip.Prefix{}, false, nil
	}
	return prefixes[0], true, nil
}

func (s *DefaultSource) writeStateFile(ctx context.Context, iface string, prefix netip.Prefix) {
	log := s.log
	if log == nil {
		log = slog.Default()
	}
	path, err := s.stateFilePath(iface)
	if err != nil {
		log.WarnContext(ctx, "pd: state file path invalid", "iface", iface, "err", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.WarnContext(ctx, "pd: create state dir failed", "iface", iface, "path", path, "err", err)
		return
	}
	data := []byte(prefix.String() + "\n")
	if err := renameio.WriteFile(path, data, 0o644); err != nil {
		log.WarnContext(ctx, "pd: write state file failed", "iface", iface, "path", path, "err", err)
	}
}

func (s *DefaultSource) stateFilePath(iface string) (string, error) {
	if err := validateIface(iface); err != nil {
		return "", err
	}
	stateDir := s.stateDir
	if stateDir == "" {
		stateDir = defaultStateDir
	}
	return filepath.Join(stateDir, "pd-"+iface), nil
}

func parsePrefixLines(data []byte) []netip.Prefix {
	prefixes := make([]netip.Prefix, 0)
	for line := range bytes.SplitSeq(data, []byte{'\n'}) {
		prefix, ok := parseIPv6PrefixText(string(line), -1)
		if ok {
			prefixes = append(prefixes, prefix)
		}
	}
	return uniqueSortedPrefixes(prefixes)
}
