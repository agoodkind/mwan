// Package moduleconfig builds configuration for the modules in an ifmgr role.
package moduleconfig

import (
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/bridgeprobe"
	"goodkind.io/mwan/internal/ifmgr/modules/cloudflaredtap"
	"goodkind.io/mwan/internal/ifmgr/modules/connprobe"
	"goodkind.io/mwan/internal/ifmgr/modules/hostipv6policy"
	"goodkind.io/mwan/internal/ifmgr/modules/mainv4"
	"goodkind.io/mwan/internal/ifmgr/modules/oobv4"
	"goodkind.io/mwan/internal/ifmgr/modules/oobv6"
	"goodkind.io/mwan/internal/ifmgr/modules/policyrules"
	"goodkind.io/mwan/internal/ifmgr/modules/ralost"
	"goodkind.io/mwan/internal/ifmgr/modules/slaachealth"
	"goodkind.io/mwan/internal/netif"
)

const (
	defaultSLAACRulePriority       = 7
	defaultSLAACDegradedAfter      = 30 * time.Second
	defaultSLAACAlertAfter         = 5 * time.Minute
	defaultRALostAfter             = 5 * time.Minute
	defaultProbeTimeout            = 2 * time.Second
	defaultUnhealthyAfter          = 10 * time.Second
	defaultNoSignalAlertAfter      = 2 * time.Minute
	defaultMissingIfaceGracePeriod = 2 * time.Minute
)

// Build prepares configuration for the modules that run in the given role.
//
// Each role validates only its own modules. For example, the WAN role can
// start without resolving user IDs for OOB policy rules.
//
//	configs, err := moduleconfig.Build(settings.IfMgr, "wan")
func Build(
	ifmgrCfg config.IfMgrSection,
	role string,
) (ifmgr.ModuleConfigSet, error) {
	modules := ifmgrCfg.Modules
	logger := slog.Default().With("component", "ifmgr")
	names, err := ifmgr.ModulesForRole(role)
	if err != nil {
		logger.Warn("ifmgr: ModulesForRole failed", "role", role, "err", err)

		return nil, fmt.Errorf("ModulesForRole(%q): %w", role, err)
	}

	want := make(map[string]bool, len(names))
	for _, name := range names {
		want[name] = true
	}

	moduleConfigs := make(ifmgr.ModuleConfigSet)
	if err := addVPPModuleConfig(moduleConfigs, want["vpp"], ifmgrCfg); err != nil {
		return nil, err
	}

	if want["oobv6"] {
		oobV6Config := buildOOBV6Config(modules.OOBV6)
		moduleConfigs["oobv6"] = oobV6Config
	}

	if want["oobv4"] {
		moduleConfigs["oobv4"] = buildOOBV4Config(modules.OOBV4)
	}

	if want["slaac_health"] {
		slaacHealthConfig, err := buildSLAACHealthConfig(modules.SLAACHealth)
		if err != nil {
			return nil, err
		}

		moduleConfigs["slaac_health"] = slaacHealthConfig
	}

	if want["ra_lost"] {
		raLostConfig, err := buildRALostConfig(modules.RALost)
		if err != nil {
			return nil, err
		}

		moduleConfigs["ra_lost"] = raLostConfig
	}

	if want["connectivity_probe"] {
		connectivityProbeConfig, err := buildConnectivityProbeConfig(modules.ConnectivityProbe)
		if err != nil {
			return nil, err
		}

		moduleConfigs["connectivity_probe"] = connectivityProbeConfig
	}

	if want["bridge_probe"] {
		bridgeProbeConfig, err := buildBridgeProbeConfig(modules.BridgeProbe)
		if err != nil {
			return nil, err
		}

		moduleConfigs["bridge_probe"] = bridgeProbeConfig
	}

	if want["cloudflared_tap"] {
		moduleConfigs["cloudflared_tap"] = buildCloudflaredTapConfig(modules.CloudflaredTap)
	}

	if want["mainv4"] {
		moduleConfigs["mainv4"] = buildMainV4Config(modules.MainV4)
	}

	if want["policy_rules"] {
		policyRulesConfig, err := buildPolicyRulesConfig(modules.PolicyRules)
		if err != nil {
			return nil, err
		}

		moduleConfigs["policy_rules"] = policyRulesConfig
	}

	if want["host_ipv6_policy"] {
		hostIPv6PolicyConfig, err := buildHostIPv6PolicyConfig(modules.HostIPv6Policy)
		if err != nil {
			return nil, err
		}

		moduleConfigs["host_ipv6_policy"] = hostIPv6PolicyConfig
	}

	if err := addWANRoleConfigs(moduleConfigs, want, ifmgrCfg); err != nil {
		return nil, err
	}

	return moduleConfigs, nil
}

func buildOOBV6Config(section *config.IfMgrOOBV6Section) oobv6.Config {
	cfg := oobv6.Config{
		Iface:             "",
		OOBAddr:           "",
		OOBTableID:        0,
		ManageSLAACRule:   true,
		SLAACRulePriority: defaultSLAACRulePriority,
	}
	if section == nil {
		return cfg
	}

	cfg.Iface = section.Iface
	cfg.OOBAddr = section.OOBAddr
	cfg.OOBTableID = section.OOBTableID
	if section.ManageSLAACSourceRule != nil {
		cfg.ManageSLAACRule = *section.ManageSLAACSourceRule
	}

	if section.SLAACRulePriority != nil {
		cfg.SLAACRulePriority = *section.SLAACRulePriority
	}

	return cfg
}

func buildOOBV4Config(section *config.IfMgrOOBV4Section) oobv4.Config {
	cfg := oobv4.Config{
		Iface:              "",
		OOBTableID:         0,
		StateFile:          "",
		SourceRulePriority: 0,
	}
	if section == nil {
		return cfg
	}

	cfg.Iface = section.Iface
	cfg.OOBTableID = section.OOBTableID
	cfg.SourceRulePriority = section.SourceRulePriority

	return cfg
}

func buildSLAACHealthConfig(section *config.IfMgrSLAACHealthSection) (slaachealth.Config, error) {
	cfg := slaachealth.Config{
		Iface:          "",
		DegradedAfter:  0,
		AlertAfter:     0,
		ProbeTargetsV6: nil,
		ProbeTimeout:   defaultProbeTimeout,
	}
	if section == nil {
		return cfg, nil
	}

	cfg.Iface = section.Iface
	if section.MaxTogglesPerHour != nil && *section.MaxTogglesPerHour != 0 {
		slog.Warn(
			"[ifmgr.modules.slaac_health] max_toggles_per_hour has no effect",
			"max_toggles_per_hour",
			*section.MaxTogglesPerHour,
		)
	}

	if section.EscalateAfter != "" {
		if _, err := parseDurationSetting(
			section.EscalateAfter,
			0,
			"ifmgr.modules.slaac_health.escalate_after",
		); err != nil {
			return slaachealth.Config{}, err
		}

		slog.Warn(
			"[ifmgr.modules.slaac_health] escalate_after has no effect",
			"escalate_after",
			section.EscalateAfter,
		)
	}

	var err error
	cfg.DegradedAfter, err = parseDurationSetting(
		section.DegradedAfter,
		defaultSLAACDegradedAfter,
		"ifmgr.modules.slaac_health.degraded_after",
	)
	if err != nil {
		return slaachealth.Config{}, err
	}

	cfg.AlertAfter, err = parseDurationSetting(
		section.AlertAfter,
		defaultSLAACAlertAfter,
		"ifmgr.modules.slaac_health.alert_after",
	)
	if err != nil {
		return slaachealth.Config{}, err
	}

	cfg.ProbeTimeout, err = parseDurationSetting(
		section.ProbeTimeout,
		cfg.ProbeTimeout,
		"ifmgr.modules.slaac_health.probe_timeout",
	)
	if err != nil {
		return slaachealth.Config{}, err
	}

	cfg.ProbeTargetsV6, err = probeTargets(
		section.Iface,
		section.ProbeTargetsV6,
		"ifmgr.modules.slaac_health.probe_targets_v6",
	)
	if err != nil {
		return slaachealth.Config{}, err
	}

	return cfg, nil
}

func probeTargets(iface string, values []string, field string) ([]netip.Addr, error) {
	targets, err := config.ParseAddrList(values, field)
	if err != nil {
		slog.Warn("Probe targets are invalid.", "interface", iface, "err", err)

		return nil, fmt.Errorf("configure probes on %s: %w", iface, err)
	}

	return targets, nil
}

func buildRALostConfig(section *config.IfMgrRALostSection) (ralost.Config, error) {
	cfg := ralost.Config{
		Iface:       "",
		RALostAfter: defaultRALostAfter,
	}
	if section == nil {
		return cfg, nil
	}

	cfg.Iface = section.Iface
	var err error
	cfg.RALostAfter, err = parseDurationSetting(
		section.RALostAlertAfter,
		cfg.RALostAfter,
		"ifmgr.modules.ra_lost.ra_lost_alert_after",
	)
	if err != nil {
		return ralost.Config{}, err
	}

	return cfg, nil
}

func buildConnectivityProbeConfig(
	section *config.IfMgrConnectivityProbeSection,
) (connprobe.Config, error) {
	cfg := connprobe.Config{
		Iface:          "",
		TargetsV4:      nil,
		TargetsV6:      nil,
		Timeout:        defaultProbeTimeout,
		UnhealthyAfter: defaultUnhealthyAfter,
	}
	if section == nil {
		return cfg, nil
	}

	cfg.Iface = section.Iface

	var err error
	cfg.Timeout, err = parseDurationSetting(
		section.Timeout,
		cfg.Timeout,
		"ifmgr.modules.connectivity_probe.timeout",
	)
	if err != nil {
		return connprobe.Config{}, err
	}

	cfg.UnhealthyAfter, err = parseDurationSetting(
		section.UnhealthyAfter,
		cfg.UnhealthyAfter,
		"ifmgr.modules.connectivity_probe.unhealthy_after",
	)
	if err != nil {
		return connprobe.Config{}, err
	}

	cfg.TargetsV4, err = probeTargets(
		section.Iface,
		section.TargetsV4,
		"ifmgr.modules.connectivity_probe.targets_v4",
	)
	if err != nil {
		return connprobe.Config{}, err
	}

	cfg.TargetsV6, err = probeTargets(
		section.Iface,
		section.TargetsV6,
		"ifmgr.modules.connectivity_probe.targets_v6",
	)
	if err != nil {
		return connprobe.Config{}, err
	}

	return cfg, nil
}

func buildBridgeProbeConfig(section *config.IfMgrBridgeProbeSection) (bridgeprobe.Config, error) {
	cfg := bridgeprobe.Config{
		Iface:              "",
		NoSignalAlertAfter: defaultNoSignalAlertAfter,
	}
	if section == nil {
		return cfg, nil
	}

	cfg.Iface = section.Iface
	var err error
	cfg.NoSignalAlertAfter, err = parseDurationSetting(
		section.NoSignalAlertAfter,
		cfg.NoSignalAlertAfter,
		"ifmgr.modules.bridge_probe.no_signal_alert_after",
	)
	if err != nil {
		return bridgeprobe.Config{}, err
	}

	return cfg, nil
}

func buildCloudflaredTapConfig(section *config.IfMgrCloudflaredTapSection) cloudflaredtap.Config {
	cfg := cloudflaredtap.Config{
		Unit:              "",
		DowngradePatterns: nil,
		JournalctlPath:    "",
	}
	if section == nil {
		return cfg
	}

	cfg.Unit = section.Unit
	cfg.DowngradePatterns = append([]string(nil), section.DowngradePatterns...)
	cfg.JournalctlPath = section.JournalctlPath

	return cfg
}

func buildMainV4Config(section *config.IfMgrMainV4Section) mainv4.Config {
	cfg := mainv4.Config{
		Iface:     "",
		StateFile: "",
	}
	if section == nil {
		return cfg
	}

	cfg.Iface = section.Iface

	return cfg
}

func buildPolicyRulesConfig(
	section *config.IfMgrPolicyRulesSection,
) (policyrules.Config, error) {
	logger := slog.Default().With("component", "ifmgr")
	cfg := policyrules.Config{
		Rules: nil,
	}
	if section == nil {
		return cfg, nil
	}

	cfg.Rules = make([]netif.DesiredRule, 0, len(section.Rule))
	for i, rule := range section.Rule {
		uidRange, err := buildPolicyRuleUIDRange(rule, lookupUserID)
		if err != nil {
			logger.Warn("ifmgr: build policy rule uid range failed", "index", i, "err", err)

			return policyrules.Config{}, fmt.Errorf("policy_rules.rule[%d]: %w", i, err)
		}

		cfg.Rules = append(
			cfg.Rules,
			netif.DesiredRule{
				Family:   rule.Family,
				Priority: rule.Priority,
				From:     rule.From,
				Mark:     0,
				IifName:  "",
				UIDRange: uidRange,
				Table:    rule.Table,
				TableID:  rule.TableID,
			},
		)
	}

	return cfg, nil
}

func buildHostIPv6PolicyConfig(
	section *config.IfMgrHostIPv6PolicySection,
) (hostipv6policy.Config, error) {
	cfg := hostipv6policy.Config{
		MissingIfaceGracePeriod: defaultMissingIfaceGracePeriod,
		Policies:                nil,
	}
	if section == nil {
		return cfg, nil
	}

	var err error
	cfg.MissingIfaceGracePeriod, err = parseDurationSetting(
		section.MissingIfaceGracePeriod,
		cfg.MissingIfaceGracePeriod,
		"ifmgr.modules.host_ipv6_policy.missing_iface_grace_period",
	)
	if err != nil {
		return hostipv6policy.Config{}, err
	}

	cfg.Policies = make([]hostipv6policy.InterfacePolicy, 0, len(section.Interface))
	for _, policy := range section.Interface {
		cfg.Policies = append(
			cfg.Policies,
			hostipv6policy.InterfacePolicy{
				Name:             policy.Name,
				AcceptRA:         policy.AcceptRA,
				AutoConf:         policy.AutoConf,
				AcceptRADefRtr:   policy.AcceptRADefRtr,
				SolicitRA:        policy.SolicitRA,
				CleanupRADefault: policy.CleanupRADefault,
			},
		)
	}

	return cfg, nil
}

func parseDurationSetting(
	raw string,
	defaultValue time.Duration,
	fieldName string,
) (time.Duration, error) {
	logger := slog.Default().With("component", "ifmgr")
	if raw == "" {
		return defaultValue, nil
	}

	durationValue, err := time.ParseDuration(raw)
	if err != nil {
		logger.Warn(
			"ifmgr: parse duration setting failed",
			"field",
			fieldName,
			"value",
			raw,
			"err",
			err,
		)

		return 0, fmt.Errorf("%s %q: %w", fieldName, raw, err)
	}

	return durationValue, nil
}
