package pd

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
)

func (s *DefaultSource) prefixFromJournal(
	ctx context.Context,
	iface string,
) (netip.Prefix, bool, error) {
	log := s.log
	if log == nil {
		log = slog.Default()
	}
	output, err := s.command(ctx, log, "journalctl", []string{"-u", "systemd-networkd", "-b"})
	if err != nil {
		log.WarnContext(ctx, "pd: journalctl failed", "iface", iface, "err", err)
		return netip.Prefix{}, false, fmt.Errorf("journalctl systemd-networkd: %w", err)
	}
	prefix, ok := parseJournalPrefix(output, iface)
	return prefix, ok, nil
}

func parseJournalPrefix(output []byte, iface string) (netip.Prefix, bool) {
	needle := []byte(iface + ": DHCP: received delegated prefix")
	var lastLine []byte
	for line := range bytes.SplitSeq(output, []byte{'\n'}) {
		if bytes.Contains(line, needle) {
			lastLine = line
		}
	}
	if len(lastLine) == 0 {
		return netip.Prefix{}, false
	}

	fields := strings.Fields(string(lastLine))
	if len(fields) == 0 {
		return netip.Prefix{}, false
	}
	prefix, ok := parseIPv6PrefixText(fields[len(fields)-1], -1)
	return prefix, ok
}
