package main

import (
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	bridgeprobe "goodkind.io/mwan/internal/ifmgr/modules/bridgeprobe"
	cloudflaredtap "goodkind.io/mwan/internal/ifmgr/modules/cloudflaredtap"
	connprobe "goodkind.io/mwan/internal/ifmgr/modules/connprobe"
	health "goodkind.io/mwan/internal/ifmgr/modules/health"
	hostipv6policy "goodkind.io/mwan/internal/ifmgr/modules/hostipv6policy"
	mainv4 "goodkind.io/mwan/internal/ifmgr/modules/mainv4"
	npt "goodkind.io/mwan/internal/ifmgr/modules/npt"
	oobv4 "goodkind.io/mwan/internal/ifmgr/modules/oobv4"
	oobv6 "goodkind.io/mwan/internal/ifmgr/modules/oobv6"
	policyrules "goodkind.io/mwan/internal/ifmgr/modules/policyrules"
	ralost "goodkind.io/mwan/internal/ifmgr/modules/ralost"
	slaachealth "goodkind.io/mwan/internal/ifmgr/modules/slaachealth"
	steering "goodkind.io/mwan/internal/ifmgr/modules/steering"
	wanroutes "goodkind.io/mwan/internal/ifmgr/modules/wanroutes"
	wg "goodkind.io/mwan/internal/ifmgr/modules/wg"
	"goodkind.io/mwan/internal/netif"
)

// buildIfMgrModuleConfigs builds module configs for ONLY the modules in the
// active role. Building is role-scoped (not build-everything) so an instanced
// daemon (e.g. mwan-ifmgr@wan) never builds or validates a module config that
// belongs to a different role sharing the same /etc/mwan/config.toml. Without
// this, an eager build of policy_rules (which resolves uid_user at build time)
// would crash @wan on a host lacking that user, even though @wan never runs
// policy_rules.
func buildIfMgrModuleConfigs(
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

	if want["wg"] {
		wgConfig, err := buildWGConfig(modules.WG)
		if err != nil {
			return nil, err
		}
		moduleConfigs["wg"] = wgConfig
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

// addWANRoleConfigs builds the wan-role module configs from the one shared
// network configuration, so health, wan.routes, steering, and npt read the same
// provider list.
func addWANRoleConfigs(
	moduleConfigs ifmgr.ModuleConfigSet,
	want map[string]bool,
	ifmgrCfg config.IfMgrSection,
) error {
	shared := buildWANRefs(ifmgrCfg)
	var routesSection *config.IfMgrWANRoutesSection
	if ifmgrCfg.Modules.WAN != nil {
		routesSection = ifmgrCfg.Modules.WAN.Routes
	}
	if want["health"] {
		healthConfig, err := buildHealthConfig(shared, ifmgrCfg.Modules.Health)
		if err != nil {
			return err
		}
		moduleConfigs["health"] = healthConfig
	}
	if want["wan.routes"] {
		wanRoutesConfig, err := buildWANRoutesConfig(shared, routesSection)
		if err != nil {
			return err
		}
		moduleConfigs["wan.routes"] = wanRoutesConfig
	}
	if want["steering"] {
		steeringConfig, err := buildSteeringConfig(shared, ifmgrCfg, routesSection)
		if err != nil {
			return err
		}
		moduleConfigs["steering"] = steeringConfig
	}
	if want["npt"] {
		moduleConfigs["npt"] = buildNPTConfig(shared)
	}
	return nil
}

// buildSteeringConfig projects the shared provider list and the group-wide
// steering settings onto the steering module config. The internal link and
// network come from the same wan.routes section the routing module reads, so
// the rules that set a mark and the rules that act on it name one link.
func buildSteeringConfig(
	shared sharedWANInputs,
	ifmgrCfg config.IfMgrSection,
	section *config.IfMgrWANRoutesSection,
) (steering.Config, error) {
	cfg := steering.Config{
		InternalIface:   "",
		InternalNetV4:   "",
		InternalPrefix:  shared.InternalPrefix,
		OpnsenseEdgeV6:  shared.OpnsenseEdgeV6,
		HashMode:        ifmgrCfg.HashMode,
		HealthStateFile: "",
		Members:         nil,
	}
	if section == nil {
		return cfg, nil
	}
	cfg.InternalIface = section.InternalIface
	cfg.InternalNetV4 = section.InternalNetV4
	cfg.HealthStateFile = section.HealthStateFile
	cfg.Members = make([]steering.Member, 0, len(shared.WANs))
	for _, wan := range shared.WANs {
		mark, err := wanFwMark(wan)
		if err != nil {
			return steering.Config{}, err
		}
		cfg.Members = append(cfg.Members, steering.Member{
			WANRef: wan.WANRef,
			Mark:   mark,
			Tier:   wan.Tier,
			Weight: wan.Weight,
		})
	}
	return cfg, nil
}

// buildHealthConfig joins shared WAN identities with keyed health policy.
func buildHealthConfig(
	shared sharedWANInputs,
	section *config.IfMgrHealthSection,
) (health.Config, error) {
	cfg := health.Config{
		StateFile:         "",
		PersistStateFile:  "",
		TargetsV4:         nil,
		TargetsV6:         nil,
		HTTPURLs:          nil,
		Timeout:           0,
		Interval:          0,
		PingCount:         0,
		SuccessThreshold:  0,
		FailureThreshold:  0,
		RecoveryThreshold: 0,
		StatusPushCID:     0,
		StatusPushPort:    0,
		WANs:              make([]health.WAN, 0, len(shared.WANs)),
	}
	if section == nil {
		for _, wan := range shared.WANs {
			cfg.WANs = append(cfg.WANs, health.WAN{
				WANRef:            wan.WANRef,
				Tier:              wan.Tier,
				TargetsV4:         nil,
				TargetsV6:         nil,
				HTTPURLs:          nil,
				PingCount:         0,
				SuccessThreshold:  0,
				FailureThreshold:  0,
				RecoveryThreshold: 0,
				CheckInterval:     0,
			})
		}
		return cfg, nil
	}

	cfg.StateFile = section.StateFile
	cfg.PersistStateFile = section.PersistStateFile
	// The watchdog's address, not a network value: it names a vsock endpoint on
	// this machine's hypervisor, which is why it comes from TOML beside the
	// state files rather than from the network tree.
	cfg.StatusPushCID = section.StatusPushCID
	cfg.StatusPushPort = section.StatusPushPort

	cfg.Timeout = time.Duration(section.ProbeTimeoutMillis) * time.Millisecond

	var err error
	for _, wan := range shared.WANs {
		wanSection, ok := section.WAN[wan.Name]
		if !ok || !wanSection.Enabled {
			continue
		}
		// Require an enabled WAN to fully specify its policy. Go's TOML cannot
		// tell an omitted scalar from an explicit 0, so the health module's
		// resolver treats 0 as "inherit the module-wide default". The rendered
		// config always populates every threshold for every WAN, so an enabled
		// WAN missing one is a config error, not an intentional inherit. Reject
		// it here rather than let it silently fall back to the public defaults.
		// The two ping-target families are the exception: a provider carries at
		// least one target across them, and a family it declares empty means it
		// holds no address there rather than that it wants the defaults.
		if err := validateHealthWANSection(wan.Name, wanSection); err != nil {
			return health.Config{}, err
		}
		healthWAN := health.WAN{
			WANRef:            wan.WANRef,
			Tier:              wan.Tier,
			TargetsV4:         nil,
			TargetsV6:         nil,
			HTTPURLs:          append([]string(nil), wanSection.HTTPURLs...),
			PingCount:         healthSettingValue(wanSection.PingCount),
			SuccessThreshold:  healthSettingValue(wanSection.SuccessThreshold),
			FailureThreshold:  healthSettingValue(wanSection.FailureThreshold),
			RecoveryThreshold: healthSettingValue(wanSection.RecoveryThreshold),
			CheckInterval:     0,
		}
		fieldPrefix := "network.json wan " + wan.Name + " health"
		healthWAN.TargetsV4, err = parseHealthTargets(
			wanSection.TargetsV4,
			fieldPrefix+"/targets-v4",
		)
		if err != nil {
			return health.Config{}, err
		}
		healthWAN.TargetsV6, err = parseHealthTargets(
			wanSection.TargetsV6,
			fieldPrefix+"/targets-v6",
		)
		if err != nil {
			return health.Config{}, err
		}
		healthWAN.CheckInterval = time.Duration(healthSettingValue(wanSection.CheckIntervalSeconds)) * time.Second
		if healthWAN.CheckInterval != 0 &&
			(cfg.Interval == 0 || healthWAN.CheckInterval < cfg.Interval) {
			cfg.Interval = healthWAN.CheckInterval
		}
		cfg.WANs = append(cfg.WANs, healthWAN)
	}
	return cfg, nil
}

// validateHealthWANSection rejects an enabled health WAN that under-specifies
// its probe policy. Every threshold must be positive, the two ping-target
// families must hold at least one entry between them, and success-threshold
// must not exceed the target count of a family that has targets. The model
// requires neither family, because a provider on an IPv4-only link carries no
// IPv6 targets; requiring both here would leave that provider unable to load.
// The target lists are the whole statement of which families a link carries,
// and are independent of npt-prefix and v4-source: a provider can carry IPv6
// with no delegated prefix, so neither of those leaves may be read as evidence
// about a family here or anywhere else.
// http-urls stays optional because HTTP is only an OR fallback leg of the
// verdict. This turns a malformed health container in network.json into a load
// error instead of a silent inherit of the module-wide defaults.
func validateHealthWANSection(name string, s config.IfMgrHealthWANSection) error {
	prefix := "network.json wan " + name + " health"
	successThreshold := healthSettingValue(s.SuccessThreshold)
	if healthSettingValue(s.PingCount) <= 0 {
		return fmt.Errorf("%s/ping-count must be > 0", prefix)
	}
	if successThreshold <= 0 {
		return fmt.Errorf("%s/success-threshold must be > 0", prefix)
	}
	if healthSettingValue(s.FailureThreshold) <= 0 {
		return fmt.Errorf("%s/failure-threshold must be > 0", prefix)
	}
	if healthSettingValue(s.RecoveryThreshold) <= 0 {
		return fmt.Errorf("%s/recovery-threshold must be > 0", prefix)
	}
	if len(s.TargetsV4) == 0 && len(s.TargetsV6) == 0 {
		return fmt.Errorf("%s must have at least one targets-v4 or targets-v6 entry", prefix)
	}
	if len(s.TargetsV4) > 0 && successThreshold > len(s.TargetsV4) {
		return fmt.Errorf(
			"%s/success-threshold %d exceeds targets-v4 count %d",
			prefix, successThreshold, len(s.TargetsV4),
		)
	}
	if len(s.TargetsV6) > 0 && successThreshold > len(s.TargetsV6) {
		return fmt.Errorf(
			"%s/success-threshold %d exceeds targets-v6 count %d",
			prefix, successThreshold, len(s.TargetsV6),
		)
	}
	return nil
}

// healthSettingValue reads one probe setting as the number the health module
// takes. An absent setting reads as zero, which the enabled-probe validation
// above refuses, so only a disabled probe, whose settings the module never
// runs, can carry one.
func healthSettingValue(setting *int) int {
	if setting == nil {
		return 0
	}
	return *setting
}

// buildNPTConfig projects the shared translation prefixes and per-WAN
// translation configuration from the network configuration into the npt module
// config. The WAN list and prefixes come from the shared inputs, so npt and
// wan.routes always agree on the same WAN set. Reading shared.MwanbrEdgeV6 here
// makes it a real consumer of the shared field.
func buildNPTConfig(shared sharedWANInputs) npt.Config {
	return npt.Config{
		InternalPrefix: shared.InternalPrefix,
		OpnsenseEdgeV6: shared.OpnsenseEdgeV6,
		MwanbrEdgeV6:   shared.MwanbrEdgeV6,
		WANs:           shared.nptWANs(),
	}
}

// buildWGConfig returns nil when section is nil so the wg module's
// constructor (wg.New) flips its disabled flag and Init returns the
// ifmgr.ErrModuleDisabled sentinel. A present but defaulted section
// renders local-exec mode on wg0, which is a valid configuration on
// suburban and must NOT trip the disabled sentinel.
func buildWGConfig(section *config.IfMgrWGHealthSection) (ifmgr.ModuleConfig, error) {
	if section == nil {
		return nil, nil
	}
	cfg := wg.Config{
		SSHHost:           "",
		SSHPort:           0,
		IdentityFile:      "",
		Iface:             "wg0",
		Sudo:              false,
		WarnHandshakeAge:  180 * time.Second,
		ErrorHandshakeAge: 300 * time.Second,
		Timeout:           10 * time.Second,
		IgnorePeers:       map[string]bool{},
	}
	cfg.SSHHost = section.SSHHost
	if section.SSHPort != nil {
		cfg.SSHPort = *section.SSHPort
	}
	cfg.IdentityFile = section.IdentityFile
	if section.Iface != "" {
		cfg.Iface = section.Iface
	}
	cfg.Sudo = section.Sudo

	var err error
	cfg.WarnHandshakeAge, err = parseDurationSetting(
		section.WarnHandshakeAge,
		cfg.WarnHandshakeAge,
		"ifmgr.modules.wg.warn_handshake_age",
	)
	if err != nil {
		return nil, err
	}
	cfg.ErrorHandshakeAge, err = parseDurationSetting(
		section.ErrorHandshakeAge,
		cfg.ErrorHandshakeAge,
		"ifmgr.modules.wg.error_handshake_age",
	)
	if err != nil {
		return nil, err
	}
	cfg.Timeout, err = parseDurationSetting(
		section.Timeout,
		cfg.Timeout,
		"ifmgr.modules.wg.timeout",
	)
	if err != nil {
		return nil, err
	}
	for _, peer := range section.IgnorePeers {
		cfg.IgnorePeers[peer] = true
	}
	return cfg, nil
}

func buildOOBV6Config(section *config.IfMgrOOBV6Section) oobv6.Config {
	cfg := oobv6.Config{
		Iface:             "",
		OOBAddr:           "",
		OOBTableID:        0,
		ManageSLAACRule:   true,
		SLAACRulePriority: 7,
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
		Iface:      "",
		OOBTableID: 0,
	}
	if section == nil {
		return cfg
	}
	cfg.Iface = section.Iface
	cfg.OOBTableID = section.OOBTableID
	return cfg
}

func buildSLAACHealthConfig(section *config.IfMgrSLAACHealthSection) (slaachealth.Config, error) {
	cfg := slaachealth.Config{
		Iface:             "",
		DegradedAfter:     0,
		EscalateAfter:     0,
		AlertAfter:        0,
		MaxTogglesPerHour: 4,
		ProbeTargetsV6:    nil,
		ProbeTimeout:      2 * time.Second,
	}
	if section == nil {
		return cfg, nil
	}
	cfg.Iface = section.Iface

	var err error
	cfg.DegradedAfter, err = parseDurationSetting(
		section.DegradedAfter,
		30*time.Second,
		"ifmgr.modules.slaac_health.degraded_after",
	)
	if err != nil {
		return slaachealth.Config{}, err
	}
	cfg.EscalateAfter, err = parseDurationSetting(
		section.EscalateAfter,
		90*time.Second,
		"ifmgr.modules.slaac_health.escalate_after",
	)
	if err != nil {
		return slaachealth.Config{}, err
	}
	cfg.AlertAfter, err = parseDurationSetting(
		section.AlertAfter,
		300*time.Second,
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
	if section.MaxTogglesPerHour != nil {
		cfg.MaxTogglesPerHour = *section.MaxTogglesPerHour
	}
	cfg.ProbeTargetsV6, err = parseAddrList(
		section.ProbeTargetsV6,
		"ifmgr.modules.slaac_health.probe_targets_v6",
	)
	if err != nil {
		return slaachealth.Config{}, err
	}
	return cfg, nil
}

func buildRALostConfig(section *config.IfMgrRALostSection) (ralost.Config, error) {
	cfg := ralost.Config{
		Iface:       "",
		RALostAfter: 5 * time.Minute,
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
		TargetsV6:      nil,
		Timeout:        2 * time.Second,
		UnhealthyAfter: 10 * time.Second,
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
	cfg.TargetsV6, err = parseAddrList(
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
		NoSignalAlertAfter: 120 * time.Second,
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
		Iface: "",
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
			logger.Warn("ifmgr: build policy rule uid range failed",
				"index", i, "err", err)
			return policyrules.Config{}, fmt.Errorf(
				"policy_rules.rule[%d]: %w", i, err,
			)
		}
		cfg.Rules = append(cfg.Rules, netif.DesiredRule{
			Family:   rule.Family,
			Priority: rule.Priority,
			From:     rule.From,
			Mark:     0,
			IifName:  "",
			UIDRange: uidRange,
			Table:    rule.Table,
			TableID:  rule.TableID,
		})
	}
	return cfg, nil
}

func buildHostIPv6PolicyConfig(
	section *config.IfMgrHostIPv6PolicySection,
) (hostipv6policy.Config, error) {
	cfg := hostipv6policy.Config{
		MissingIfaceGracePeriod: 2 * time.Minute,
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
		cfg.Policies = append(cfg.Policies, hostipv6policy.InterfacePolicy{
			Name:             policy.Name,
			AcceptRA:         policy.AcceptRA,
			AutoConf:         policy.AutoConf,
			AcceptRADefRtr:   policy.AcceptRADefRtr,
			SolicitRA:        policy.SolicitRA,
			CleanupRADefault: policy.CleanupRADefault,
		})
	}
	return cfg, nil
}

// sharedWAN is one WAN's full config from its network.json wan container: the
// identity (WANRef), the policy-routing slots and static mappings wan.routes
// consumes, and the steering properties the balancer reads. npt reads only the
// embedded WANRef. One home per WAN.
type sharedWAN struct {
	ifmgr.WANRef
	TableID        int
	FwMark         int
	FwMarkPrio     int
	FromPrio       int
	NptPrefix      string
	V4Source       string
	Tier           uint8
	Weight         int
	StaticMappings []config.StaticMapping
}

// sharedWANInputs is the runtime projection of the network configuration's WAN
// map and translation prefixes that every ifmgr module builder reuses. WANs is sorted by name
// for deterministic output. Each module builder projects the fields it needs.
type sharedWANInputs struct {
	WANs           []sharedWAN
	InternalPrefix string
	OpnsenseEdgeV6 string
	MwanbrEdgeV6   string
}

// nptWANs projects the shared WAN list down to what npt consumes: each
// provider's identity plus the translation prefix the configuration assigns
// it, which is what tells npt whether a missing live delegation is a fault.
func (s sharedWANInputs) nptWANs() []npt.WAN {
	wans := make([]npt.WAN, 0, len(s.WANs))
	for _, wan := range s.WANs {
		wans = append(wans, npt.WAN{WANRef: wan.WANRef, NptPrefix: wan.NptPrefix})
	}
	return wans
}

// buildWANRefs turns the network configuration's WAN map and translation
// prefixes into the shared runtime pieces module builders consume: the per-WAN
// list (sorted by name) and the shared prefixes.
func buildWANRefs(ifmgrCfg config.IfMgrSection) sharedWANInputs {
	inputs := sharedWANInputs{
		WANs:           make([]sharedWAN, 0, len(ifmgrCfg.WAN)),
		InternalPrefix: ifmgrCfg.InternalPrefix,
		OpnsenseEdgeV6: ifmgrCfg.OpnsenseEdgeV6,
		MwanbrEdgeV6:   ifmgrCfg.MwanbrEdgeV6,
	}
	names := make([]string, 0, len(ifmgrCfg.WAN))
	for name := range ifmgrCfg.WAN {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		entry := ifmgrCfg.WAN[name]
		inputs.WANs = append(inputs.WANs, sharedWAN{
			WANRef:         ifmgr.WANRef{Name: name, Iface: entry.Iface},
			TableID:        entry.TableID,
			FwMark:         entry.FwMark,
			FwMarkPrio:     entry.FwMarkPrio,
			FromPrio:       entry.FromPrio,
			NptPrefix:      entry.NptPrefix,
			V4Source:       entry.V4Source,
			Tier:           entry.Tier,
			Weight:         entry.Weight,
			StaticMappings: entry.StaticMappings,
		})
	}
	return inputs
}

func buildWANRoutesConfig(
	shared sharedWANInputs,
	section *config.IfMgrWANRoutesSection,
) (wanroutes.Config, error) {
	cfg := wanroutes.Config{
		InternalIface:   "",
		OpnsenseEdgeV6:  "",
		InternalNetV4:   "",
		HealthStateFile: "",
		WANs:            nil,
	}
	if section == nil {
		return cfg, nil
	}
	cfg.InternalIface = section.InternalIface
	cfg.OpnsenseEdgeV6 = shared.OpnsenseEdgeV6
	cfg.InternalNetV4 = section.InternalNetV4
	cfg.HealthStateFile = section.HealthStateFile
	cfg.WANs = make([]wanroutes.WAN, 0, len(shared.WANs))
	for _, wan := range shared.WANs {
		mark, err := wanFwMark(wan)
		if err != nil {
			return wanroutes.Config{}, err
		}
		cfg.WANs = append(cfg.WANs, wanroutes.WAN{
			WANRef:          wan.WANRef,
			TableID:         wan.TableID,
			FwMark:          mark,
			FwMarkPrio:      wan.FwMarkPrio,
			FromPrio:        wan.FromPrio,
			NptPrefix:       wan.NptPrefix,
			V4Source:        wan.V4Source,
			Tier:            wan.Tier,
			Weight:          wan.Weight,
			MappedExternals: mappedExternals(wan.StaticMappings),
		})
	}
	return cfg, nil
}

// mappedExternals projects a provider's static mappings onto the external
// addresses the routing module decides link ownership for. The routing module
// never translates, so the internal half stays with the firewall render.
func mappedExternals(mappings []config.StaticMapping) []netip.Addr {
	if len(mappings) == 0 {
		return nil
	}
	externals := make([]netip.Addr, 0, len(mappings))
	for _, mapping := range mappings {
		externals = append(externals, mapping.External)
	}
	return externals
}

// wanFwMark narrows one provider's firewall mark onto the kernel's width. The
// model bounds the leaf at 1 or higher and the loader refuses a missing value,
// so the only case left is a value too wide for a mark. Both the routing module
// and the steering module go through here, so a mark either module refuses is
// refused by both.
func wanFwMark(wan sharedWAN) (uint32, error) {
	if wan.FwMark < 0 {
		return 0, fmt.Errorf("network.json wan %s fw-mark must be >= 0", wan.Name)
	}
	if uint64(wan.FwMark) > uint64(^uint32(0)) {
		return 0, fmt.Errorf("network.json wan %s fw-mark %d exceeds uint32", wan.Name, wan.FwMark)
	}
	return uint32(wan.FwMark), nil
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
		logger.Warn("ifmgr: parse duration setting failed",
			"field", fieldName, "value", raw, "err", err)
		return 0, fmt.Errorf("%s %q: %w", fieldName, raw, err)
	}
	return durationValue, nil
}

// parseHealthTargets parses one ping-target family, keeping a family the
// provider left out distinguishable from one it declared empty. The health
// module inherits its module-wide targets for a nil list and probes nothing for
// an empty one, so flattening the two would ping public resolvers out a link
// that carries no address in that family.
func parseHealthTargets(values []string, fieldName string) ([]netip.Addr, error) {
	if values == nil {
		return nil, nil
	}
	return parseAddrList(values, fieldName)
}

func parseAddrList(values []string, fieldName string) ([]netip.Addr, error) {
	logger := slog.Default().With("component", "ifmgr")
	addresses := make([]netip.Addr, 0, len(values))
	for i, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil {
			logger.Warn("ifmgr: parse address failed",
				"field", fieldName, "index", i, "value", value, "err", err)
			return nil, fmt.Errorf("%s[%d] %q: %w", fieldName, i, value, err)
		}
		addresses = append(addresses, address)
	}
	return addresses, nil
}
