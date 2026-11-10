//go:build !linux || !cgo || !nativeoffload

package nativeoffload

import "fmt"

// ValidateSupport rejects enabled native mode before any daemon mutation.
func ValidateSupport(configuration Config) error {
	if configuration.Enabled {
		return fmt.Errorf("nativeoffload requires a Linux cgo build with the nativeoffload tag and supplied patched SDK")
	}
	return nil
}
