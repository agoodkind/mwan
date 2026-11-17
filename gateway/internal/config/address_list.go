package config

import (
	"fmt"
	"log/slog"
	"net/netip"
)

// ParseAddrList parses a list of configured IP addresses.
//
// An invalid entry produces an error with the field name and list index.
//
//	targets, err := config.ParseAddrList(section.TargetsV4, "targets-v4")
func ParseAddrList(values []string, fieldName string) ([]netip.Addr, error) {
	logger := slog.Default().With("component", "ifmgr")

	addresses := make([]netip.Addr, 0, len(values))
	for index, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil {
			logger.Warn(
				"ifmgr: parse address failed",
				"field",
				fieldName,
				"index",
				index,
				"value",
				value,
				"err",
				err,
			)

			return nil, fmt.Errorf("%s[%d] %q: %w", fieldName, index, value, err)
		}

		addresses = append(addresses, address)
	}

	return addresses, nil
}
