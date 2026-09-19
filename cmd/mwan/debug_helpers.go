package main

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sort"

	"goodkind.io/mwan/internal/config"
)

type debugWAN struct {
	Name    string
	Iface   string
	TableID int
	FwMark  int
}

func debugWANs(cfg *config.Config) []debugWAN {
	names := make([]string, 0, len(cfg.IfMgr.WAN))
	for name := range cfg.IfMgr.WAN {
		names = append(names, name)
	}
	sort.Strings(names)

	wans := make([]debugWAN, 0, len(names))
	for _, name := range names {
		entry := cfg.IfMgr.WAN[name]
		wans = append(wans, debugWAN{
			Name:    name,
			Iface:   entry.Iface,
			TableID: entry.TableID,
			FwMark:  entry.FwMark,
		})
	}
	return wans
}

func debugWrappedError(logger *slog.Logger, message string, err error) error {
	logger.Warn(message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}

func debugIPv4Source(logger *slog.Logger, rawPrefix string) (string, error) {
	prefix, err := netip.ParsePrefix(rawPrefix)
	if err != nil {
		message := fmt.Sprintf("parse IPv4 source prefix %q", rawPrefix)
		return "", debugWrappedError(logger, message, err)
	}
	if !prefix.Addr().Is4() {
		return "", fmt.Errorf("IPv4 source prefix %q is not IPv4", rawPrefix)
	}
	source := prefix.Masked().Addr().Next().Next()
	if !source.IsValid() || !prefix.Contains(source) {
		return "", fmt.Errorf("IPv4 source prefix %q has no second host address", rawPrefix)
	}
	return source.String(), nil
}
