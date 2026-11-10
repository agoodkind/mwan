package main

import (
	"fmt"
	"log/slog"
	"slices"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/nativeoffload"
)

func validateNativeOffloadStartup(role string, settings config.IfMgrSection) error {
	if role != "wan" {
		return nil
	}
	_, err := buildNativeOffloadConfig(buildWANRefs(settings), settings.Modules.NativeOffload)
	return err
}

func buildNativeOffloadConfig(shared sharedWANInputs, section *config.IfMgrNativeOffloadSection) (nativeoffload.Config, error) {
	var result nativeoffload.Config
	settings, err := section.Resolve()
	if err != nil {
		slog.Warn("native offload process settings invalid", "err", err)
		return result, fmt.Errorf("nativeoffload process settings: %w", err)
	}
	result.Settings = settings
	result.Enabled = section != nil && section.Enabled
	for _, wan := range shared.WANs {
		mark, err := wanFwMark(wan)
		if err != nil {
			return nativeoffload.Config{}, err
		}
		translation := wan.TranslationV4
		if translation != nil {
			copied := *translation
			copied.StaticMappings = slices.Clone(translation.StaticMappings)
			translation = &copied
		}
		result.WANs = append(result.WANs, nativeoffload.WAN{WANRef: wan.WANRef, Mark: mark, TranslationV4: translation})
	}
	if err := nativeoffload.ValidateSupport(result); err != nil {
		slog.Warn("native offload build support unavailable", "err", err)
		return nativeoffload.Config{}, fmt.Errorf("nativeoffload configuration: %w", err)
	}
	return result, nil
}
