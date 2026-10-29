//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/offload"
)

// ValidateSupport checks the linked ABI without initializing EAL or opening devices.
func ValidateSupport(configuration Config) error {
	if !configuration.Enabled {
		return nil
	}
	if _, err := offload.ReadNativeBuild(); err != nil {
		slog.Warn("native offload linked ABI unavailable", "err", err)
		return fmt.Errorf("nativeoffload support: %w", err)
	}
	return nil
}
