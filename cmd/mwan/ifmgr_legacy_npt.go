package main

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/netif"
)

func ifMgrRole(cfg *config.Config, flags ifmgrFlags) (string, error) {
	role := cfg.IfMgr.Role
	if flags.role != "" {
		role = flags.role
	}
	if role == "" {
		return "", fmt.Errorf("ifmgr: role required (set [ifmgr].role in config or pass --role)")
	}
	return role, nil
}

func runLegacyNPTTransition(ctx context.Context, log *slog.Logger, cfg *config.Config, flags ifmgrFlags, role string) (resultErr error) {
	defer func() {
		if resultErr != nil {
			log.ErrorContext(ctx, "legacy NPT transition rejected", "err", resultErr)
		}
	}()
	if role != "wan" || flags.dryRun {
		return fmt.Errorf("legacy NPT transition requires the WAN role without dry-run")
	}
	if flags.captureLegacyNPT != "" && flags.adoptLegacyNPT != "" {
		return fmt.Errorf("capture and apply are separate operations")
	}
	loaded, err := parseNetworkConfig(ctx, log, cfg, role)
	if err != nil {
		return err
	}
	if len(loaded.Rejected) != 0 {
		return fmt.Errorf("legacy NPT transition requires every configured provider to be accepted")
	}
	if cfg.IfMgr.Modules.Addresses == nil || cfg.IfMgr.Modules.Addresses.StateFile == "" {
		return fmt.Errorf("legacy NPT transition requires the configured address journal")
	}
	configs, err := buildIfMgrModuleConfigs(cfg.IfMgr, "wan")
	if err != nil {
		return err
	}
	nptConfig, ok := configs["npt"].(npt.Config)
	if !ok {
		return fmt.Errorf("legacy NPT transition requires configured NPT")
	}
	edges, err := npt.ReadLegacyNPTEdges(ctx, log, nptConfig, cfg.IfMgr.Connections)
	if err != nil {
		return fmt.Errorf("read legacy NPT transition: %w", err)
	}
	journal := cfg.IfMgr.Modules.Addresses.StateFile
	if flags.captureLegacyNPT != "" {
		if err := netif.WriteLegacyNPTTransition(flags.captureLegacyNPT, flags.producerPID, flags.producerSHA256, journal, edges); err != nil {
			return fmt.Errorf("capture legacy NPT transition: %w", err)
		}
		return nil
	}
	if err := netif.ApplyLegacyNPTTransition(flags.adoptLegacyNPT, journal, edges); err != nil {
		return fmt.Errorf("adopt legacy NPT transition: %w", err)
	}
	return nil
}
