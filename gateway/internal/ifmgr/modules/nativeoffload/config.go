// Package nativeoffload defines the optional native admission configuration contract.
package nativeoffload

import (
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
)

// ProcessSettings reuses the loader's checked process resources and deadlines.
type ProcessSettings = config.NativeOffloadProcessSettings

// Config combines process settings with the existing provider policy projection.
type Config struct {
	Enabled  bool
	Settings ProcessSettings
	WANs     []WAN
}

// WAN preserves the connection identity and existing mark and IPv4 translation.
type WAN struct {
	ifmgr.WANRef
	Mark          uint32
	TranslationV4 *config.IPv4Translation
}

// ModuleConfigName associates the configuration with the future admission module.
func (Config) ModuleConfigName() string { return "nativeoffload" }
