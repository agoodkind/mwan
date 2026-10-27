// Package nativeoffload admits eligible Linux connections to the native packet worker.
package nativeoffload

import (
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/firewall"
	"goodkind.io/mwan/internal/ifmgr"
)

// ProcessSettings reuses the loader's checked process resources and deadlines.
type ProcessSettings = config.NativeOffloadProcessSettings

// Config combines process settings with the existing provider policy projection.
type Config struct {
	Enabled  bool
	Settings ProcessSettings
	WANs     []WAN
	Firewall firewall.Config
}

// WAN preserves the connection identity and existing mark and IPv4 translation.
type WAN struct {
	ifmgr.WANRef
	Mark          uint32
	TranslationV4 *config.IPv4Translation
}

// ModuleConfigName selects the native admission module.
func (Config) ModuleConfigName() string { return "nativeoffload" }
