package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/cloudflaredtap"
	"goodkind.io/mwan/internal/ifmgr/modules/health"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/ifmgr/modules/oobv4"
	"goodkind.io/mwan/internal/ifmgr/modules/oobv6"
	"goodkind.io/mwan/internal/ifmgr/modules/wanroutes"
	"goodkind.io/mwan/internal/wanconfig"
	"goodkind.io/mwan/internal/wanstate"
	"goodkind.io/mwan/internal/yangpub"
)

// wanconfigPublishTimeout bounds the startup publish so a stalled
// datastore cannot delay the daemon's main loop.
const wanconfigPublishTimeout = 10 * time.Second

// wanconfigSurface is the running management surface: the snapshot store
// the modules write, the open datastore connection whose provider
// registrations serve reads, the agent poller feeding BGP state, and the
// notifier streaming transitions.
type wanconfigSurface struct {
	store *wanstate.Store
	pub   yangpub.Publisher
	log   *slog.Logger
	stop  context.CancelFunc
	// senderDone closes when the notifier's sender goroutine has
	// returned. Close waits on it before releasing the connection,
	// because a send still inside the binding when the connection is
	// freed crashes the process.
	senderDone <-chan struct{}
}

// Close stops the poller and the notifier, waits for the sender to leave
// the binding, and releases the datastore connection with its
// registrations. Safe on a surface whose parts partly failed. The wait
// is bounded: a send blocks at most for sysrepo's internal subscriber
// timeout.
func (s *wanconfigSurface) Close() {
	if s.stop != nil {
		s.stop()
	}
	if s.senderDone != nil {
		<-s.senderDone
	}
	if s.pub != nil {
		if err := s.pub.Close(); err != nil {
			s.log.Error("wanconfig: datastore close failed", "err", err)
		}
	}
}

// startWanconfigSurface publishes the configuration the daemon just
// loaded into the wanconfig management datastore and registers the
// operational providers that serve live state, when this host's config
// turns the gate on and the role carries the wan modules. It returns nil
// when the host serves no surface. Every failure is logged and swallowed:
// describing the system is not a precondition for running it, so the
// daemon starts identically whether or not the surface came up.
func startWanconfigSurface(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	moduleConfigs ifmgr.ModuleConfigSet,
) *wanconfigSurface {
	if !cfg.Wanconfig.Publish {
		return nil
	}
	log := logger.With("component", "wanconfig")

	gateway, ok, err := gatewayFromModuleConfigs(cfg, moduleConfigs)
	if err != nil {
		log.ErrorContext(ctx, "wanconfig: projection from module configs failed; running without a management surface", "err", err)
		return nil
	}
	if !ok {
		log.InfoContext(ctx, "wanconfig: publish enabled but this role carries no wan config; nothing to publish")
		return nil
	}

	pub, err := yangpub.New(log)
	if err != nil {
		log.ErrorContext(ctx, "wanconfig: datastore unavailable; running without a management surface", "err", err)
		return nil
	}

	publishCtx, cancel := context.WithTimeout(ctx, wanconfigPublishTimeout)
	defer cancel()
	// Publish logs its own failure detail; the daemon carries on either way.
	_ = wanconfig.Publish(publishCtx, log, runningReplacer{pub: pub}, gateway)
	// Ownership is startup work like the publish, so it carries the same
	// bound: the daemon's main loop must not wait on the datastore.
	ownCtx, ownCancel := context.WithTimeout(ctx, wanconfigPublishTimeout)
	ownPublishedModules(ownCtx, log, pub)
	ownCancel()

	store := wanstate.New()
	surfaceCtx, stopSurface := context.WithCancel(ctx)
	surface := &wanconfigSurface{
		store:      store,
		pub:        pub,
		log:        log,
		stop:       stopSurface,
		senderDone: nil,
	}
	// The notifier observes the store before any module writes, so the
	// first real transition already streams.
	notifier := newSurfaceNotifier(log, gateway)
	store.Observe(notifier)
	surface.senderDone = startNotifierSender(surfaceCtx, log, notifier, pub)
	if err := registerLiveStateProviders(ctx, log, pub, store, gateway); err != nil {
		log.ErrorContext(ctx, "wanconfig: provider registration failed; serving configuration only", "err", err)
		return surface
	}
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(surfaceCtx,
					"wanconfig: agent poller panicked; routing-session state goes stale",
					"err", fmt.Sprint(recovered))
			}
		}()
		pollAgentBGP(surfaceCtx, log, cfg, store)
	}()
	log.InfoContext(ctx, "wanconfig: live-state providers registered",
		"members", len(gateway.Members))
	return surface
}

// daemonSettings projects the daemon settings the loaded configuration
// carries: the rollback watchdog's policy from the monolith config, and
// the out-of-band and tap module configs when this role runs them. A
// section the configuration does not carry stays absent, so the tree
// never invents values another host owns.
func daemonSettings(cfg *config.Config, configs ifmgr.ModuleConfigSet) wanconfig.DaemonSettings {
	settings := wanconfig.DaemonSettings{
		Watchdog: watchdogSettings(cfg),
		OOB:      oobSettings(configs),
		Tap:      tapSettings(configs),
	}
	return settings
}

// watchdogSettings projects the watchdog section. The rendered section
// always names the unit it drives, so the name doubles as presence.
func watchdogSettings(cfg *config.Config) wanconfig.WatchdogSettings {
	var none wanconfig.WatchdogSettings
	if cfg == nil || cfg.Watchdog.ServiceName == "" {
		return none
	}
	watchdog := wanconfig.WatchdogSettings{
		Present:                      true,
		DeployWindowMinutes:          clampUint16(cfg.Watchdog.DeployWindowMinutes),
		ConnectivityTimeoutSeconds:   clampUint16(cfg.Watchdog.ConnectivityTimeoutSeconds),
		CheckIntervalHealthySeconds:  clampUint16(cfg.Watchdog.CheckIntervalHealthy),
		CheckIntervalDegradedSeconds: clampUint16(cfg.Watchdog.CheckIntervalDegraded),
		PostRollbackGraceSeconds:     clampUint16(cfg.Watchdog.PostRollbackGraceSeconds),
		AlertCooldownSeconds:         clampUint16(cfg.Watchdog.AlertCooldownSeconds),
		DeployGracePeriodSeconds:     clampUint16(cfg.Watchdog.DeployGracePeriodSeconds),
		MaxRollbackAttempts:          clampUint8(cfg.Watchdog.MaxRollbackAttempts),
		SnapshotHealthyThreshold:     clampUint16(cfg.Watchdog.SnapshotHealthyThreshold),
		MaxKnownGoodSnapshots:        clampUint8(cfg.Watchdog.MaxKnownGoodSnapshots),
		PingTargets:                  nil,
	}
	// The watchdog probes exactly these two targets, one per family.
	for _, raw := range []string{cfg.Network.PingTargetIPv6, cfg.Network.PingTargetIPv4} {
		if raw == "" {
			continue
		}
		address, err := netip.ParseAddr(raw)
		if err != nil {
			slog.Warn("wanconfig: watchdog ping target unparsable; not published",
				"value", raw, "err", err)
			continue
		}
		watchdog.PingTargets = append(watchdog.PingTargets, address)
	}
	return watchdog
}

// oobSettings projects the out-of-band module configs this role runs.
func oobSettings(configs ifmgr.ModuleConfigSet) wanconfig.OOBSettings {
	var oob wanconfig.OOBSettings
	if v6, ok := configs["oobv6"].(oobv6.Config); ok && v6.Iface != "" {
		oob.V6Present = true
		oob.V6Iface = v6.Iface
		oob.V6TableID = clampUint32(v6.OOBTableID)
		oob.ManageSLAACRule = v6.ManageSLAACRule
		oob.SLAACRulePriority = clampUint32(v6.SLAACRulePriority)
		if v6.OOBAddr != "" {
			address, err := netip.ParseAddr(v6.OOBAddr)
			if err != nil {
				slog.Warn("wanconfig: oob v6 address unparsable; not published",
					"value", v6.OOBAddr, "err", err)
			} else {
				oob.V6Addr = address
			}
		}
	}
	if v4, ok := configs["oobv4"].(oobv4.Config); ok && v4.Iface != "" {
		oob.V4Present = true
		oob.V4Iface = v4.Iface
		oob.V4TableID = clampUint32(v4.OOBTableID)
	}
	return oob
}

// tapSettings projects the tunnel-tap module config this role runs.
func tapSettings(configs ifmgr.ModuleConfigSet) wanconfig.TapSettings {
	var tap wanconfig.TapSettings
	if section, ok := configs["cloudflared_tap"].(cloudflaredtap.Config); ok && section.Unit != "" {
		tap.Present = true
		tap.Unit = section.Unit
		tap.DowngradePatterns = append([]string(nil), section.DowngradePatterns...)
	}
	return tap
}

// clampUint16 narrows a config int onto the model's leaf range.
func clampUint16(value int) uint16 {
	if value < 0 {
		return 0
	}
	if value > int(^uint16(0)) {
		return ^uint16(0)
	}
	return uint16(value)
}

// clampUint8 narrows a config int onto the model's leaf range.
func clampUint8(value int) uint8 {
	if value < 0 {
		return 0
	}
	if value > int(^uint8(0)) {
		return ^uint8(0)
	}
	return uint8(value)
}

// clampUint32 narrows a config int onto the model's leaf range. The
// comparison widens to uint64 so the bound stays representable where
// int is 32 bits.
func clampUint32(value int) uint32 {
	if value < 0 {
		return 0
	}
	if uint64(value) > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(value)
}

// runningReplacer adapts the sysrepo binding to the one capability the
// projection package asks for: replace the owned subtrees of the running
// datastore in a single transaction.
type runningReplacer struct {
	pub yangpub.Publisher
}

// ReplaceConfig implements wanconfig.Publisher over the binding.
func (r runningReplacer) ReplaceConfig(ctx context.Context, ownedPaths []string, items []wanconfig.Item) error {
	published := make([]yangpub.Item, 0, len(items))
	for _, item := range items {
		published = append(published, yangpub.Item{Path: item.Path, Value: item.Value})
	}
	if err := r.pub.ReplaceItems(ctx, yangpub.DatastoreRunning, ownedPaths, published); err != nil {
		slog.ErrorContext(ctx, "wanconfig: datastore replace failed",
			"items", len(published), "err", err)
		return fmt.Errorf("replace running config: %w", err)
	}
	return nil
}

// gatewayFromModuleConfigs projects the wan role's typed runtime module
// configs onto the gateway the surface publishes. It reads the same values
// the daemon is about to run with: the member list, routing numbers, and
// translation prefixes from the wan.routes config, the internal translation
// prefix and edge addresses from the npt config, which members are probed and
// the probe timeout from the health config, the values no module config holds
// from the loaded network configuration, and the daemon settings the loaded
// monolith configuration carries. It returns ok=false when the set carries no
// wan.routes config, which is every role but wan; that is a quiet no-publish,
// not an error.
func gatewayFromModuleConfigs(cfg *config.Config, configs ifmgr.ModuleConfigSet) (wanconfig.Gateway, bool, error) {
	var none wanconfig.Gateway
	routesCfg, isRoutes := configs["wan.routes"].(wanroutes.Config)
	if !isRoutes || len(routesCfg.WANs) == 0 {
		return none, false, nil
	}

	group, err := groupSettings(cfg, routesCfg, configs)
	if err != nil {
		return none, false, err
	}

	probed := map[string]bool{}
	if healthCfg, isHealth := configs["health"].(health.Config); isHealth {
		for _, wan := range healthCfg.WANs {
			probed[wan.Name] = true
		}
	}

	gateway := wanconfig.Gateway{
		InternalIface: routesCfg.InternalIface,
		HashMode:      hashModeFromConfig(cfg),
		Group:         group,
		Members:       make([]wanconfig.Member, 0, len(routesCfg.WANs)),
		Daemon:        daemonSettings(cfg, configs),
	}
	for _, wan := range routesCfg.WANs {
		member, err := memberFromWAN(cfg, wan, group.InternalPrefix, probed[wan.Name])
		if err != nil {
			return none, false, err
		}
		gateway.Members = append(gateway.Members, member)
	}
	return gateway, true, nil
}

// memberFromWAN projects one provider: its identity, steering properties, and
// routing numbers from the wan.routes entry, its forced DSCP value and probe
// from the loaded network configuration, and its translation pair from its own
// prefix and the group's internal prefix.
func memberFromWAN(
	cfg *config.Config,
	wan wanroutes.WAN,
	internalPrefix netip.Prefix,
	probed bool,
) (wanconfig.Member, error) {
	logger := slog.Default().With("component", "wanconfig")
	var none wanconfig.Member
	probe, err := probeSettings(cfg, wan.Name)
	if err != nil {
		return none, err
	}
	member := wanconfig.Member{
		Name:           wan.Name,
		Iface:          wan.Iface,
		Tier:           wan.Tier,
		Weight:         clampUint16(wan.Weight),
		ProbePolicy:    "",
		NPTInternal:    netip.Prefix{},
		NPTExternal:    netip.Prefix{},
		TableID:        clampUint32(wan.TableID),
		FwMark:         wan.FwMark,
		FwMarkPrio:     clampUint32(wan.FwMarkPrio),
		FromPrio:       clampUint32(wan.FromPrio),
		V4Source:       wan.V4Source,
		ForcedDSCP:     forcedDSCPFromConfig(cfg, wan.Name),
		StaticMappings: staticMappingsFromConfig(cfg, wan.Name),
		Health:         probe,
	}
	if probed {
		// The probe policy is named after the member: the health module
		// keys its per-member policy by the same name.
		member.ProbePolicy = wan.Name
	}
	if wan.NptPrefix != "" {
		external, err := netip.ParsePrefix(wan.NptPrefix)
		if err != nil {
			logger.Warn("wanconfig: member npt prefix unparsable",
				"member", wan.Name, "value", wan.NptPrefix, "err", err)
			return none, fmt.Errorf("wanconfig: member %s npt prefix %q: %w", wan.Name, wan.NptPrefix, err)
		}
		member.NPTInternal = internalPrefix
		member.NPTExternal = external
	}
	return member, nil
}

// groupSettings projects the steering group's network values from the module
// configs that hold them, and the reserved tables from the loaded network
// configuration, which no module config carries. A value no config holds stays
// zero and publishes nothing.
func groupSettings(
	cfg *config.Config,
	routesCfg wanroutes.Config,
	configs ifmgr.ModuleConfigSet,
) (wanconfig.GroupSettings, error) {
	var none wanconfig.GroupSettings
	group := wanconfig.GroupSettings{
		ReservedTables:     reservedTablesFromConfig(cfg),
		InternalPrefix:     netip.Prefix{},
		OpnsenseEdgeV6:     netip.Addr{},
		MwanbrEdgeV6:       netip.Addr{},
		InternalNetV4:      netip.Prefix{},
		ProbeTimeoutMillis: 0,
	}
	var err error
	if nptCfg, isNPT := configs["npt"].(npt.Config); isNPT {
		if group.InternalPrefix, err = parseGroupPrefix("internal prefix", nptCfg.InternalPrefix); err != nil {
			return none, err
		}
		// npt is the only module config that holds the gateway's own edge
		// address.
		if group.MwanbrEdgeV6, err = parseGroupAddr("mwanbr edge address", nptCfg.MwanbrEdgeV6); err != nil {
			return none, err
		}
	}
	if group.OpnsenseEdgeV6, err = parseGroupAddr("router edge address", routesCfg.OpnsenseEdgeV6); err != nil {
		return none, err
	}
	if group.InternalNetV4, err = parseGroupPrefix("internal IPv4 network", routesCfg.InternalNetV4); err != nil {
		return none, err
	}
	if healthCfg, isHealth := configs["health"].(health.Config); isHealth {
		group.ProbeTimeoutMillis = clampUint32(int(healthCfg.Timeout / time.Millisecond))
	}
	return group, nil
}

// parseGroupPrefix parses one group prefix the loaded configuration holds as
// text. Empty is a value no config carries and publishes nothing; an
// unparsable value is an error rather than a leaf silently missing.
func parseGroupPrefix(what string, value string) (netip.Prefix, error) {
	if value == "" {
		return netip.Prefix{}, nil
	}
	prefix, err := netip.ParsePrefix(value)
	if err != nil {
		slog.Warn("wanconfig: group prefix unparsable", "leaf", what, "value", value, "err", err)
		return netip.Prefix{}, fmt.Errorf("wanconfig: %s %q: %w", what, value, err)
	}
	return prefix, nil
}

// parseGroupAddr is parseGroupPrefix for an address.
func parseGroupAddr(what string, value string) (netip.Addr, error) {
	if value == "" {
		return netip.Addr{}, nil
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		slog.Warn("wanconfig: group address unparsable", "leaf", what, "value", value, "err", err)
		return netip.Addr{}, fmt.Errorf("wanconfig: %s %q: %w", what, value, err)
	}
	return address, nil
}

// reservedTablesFromConfig reads the reserved routing tables from the loaded
// network configuration. The only reader of the set is the loader's own check,
// so no module config holds it. A nil configuration publishes none.
func reservedTablesFromConfig(cfg *config.Config) []uint32 {
	if cfg == nil {
		return nil
	}
	tables := make([]uint32, 0, len(cfg.IfMgr.ReservedTables))
	for _, table := range cfg.IfMgr.ReservedTables {
		tables = append(tables, clampUint32(table))
	}
	return tables
}

// forcedDSCPFromConfig reads a provider's forced DSCP value from the loaded
// network configuration. The firewall is rendered from inventory rather than
// by a module, so no module config holds the value. Zero means none.
func forcedDSCPFromConfig(cfg *config.Config, name string) uint8 {
	if cfg == nil {
		return 0
	}
	return clampUint8(cfg.IfMgr.WAN[name].ForcedDSCP)
}

// staticMappingsFromConfig reads a provider's static mappings from the loaded
// network configuration. wan.routes holds only the external half of each
// mapping, so the loaded entry is the one holder of both addresses. A nil
// configuration publishes none.
func staticMappingsFromConfig(cfg *config.Config, name string) []wanconfig.StaticMapping {
	if cfg == nil {
		return nil
	}
	loaded := cfg.IfMgr.WAN[name].StaticMappings
	if len(loaded) == 0 {
		return nil
	}
	mappings := make([]wanconfig.StaticMapping, 0, len(loaded))
	for _, mapping := range loaded {
		mappings = append(mappings, wanconfig.StaticMapping{External: mapping.External, Internal: mapping.Internal})
	}
	return mappings
}

// probeSettings projects one provider's probe from the loaded health section.
// The health module config holds only enabled probes, so the section is the one
// holder of a disabled probe's settings and of whether a provider carries a
// health container at all. A nil configuration, or a provider with no
// container, publishes no probe.
func probeSettings(cfg *config.Config, name string) (*wanconfig.ProbeSettings, error) {
	if cfg == nil || cfg.IfMgr.Modules.Health == nil {
		return nil, nil
	}
	section, present := cfg.IfMgr.Modules.Health.WAN[name]
	if !present {
		return nil, nil
	}
	fieldPrefix := "network.json wan " + name + " health"
	targetsV4, err := parseAddrList(section.TargetsV4, fieldPrefix+"/targets-v4")
	if err != nil {
		return nil, err
	}
	targetsV6, err := parseAddrList(section.TargetsV6, fieldPrefix+"/targets-v6")
	if err != nil {
		return nil, err
	}
	probe := &wanconfig.ProbeSettings{
		Enabled:              section.Enabled,
		PingCount:            uint8Setting(section.PingCount),
		SuccessThreshold:     uint8Setting(section.SuccessThreshold),
		FailureThreshold:     uint8Setting(section.FailureThreshold),
		RecoveryThreshold:    uint8Setting(section.RecoveryThreshold),
		CheckIntervalSeconds: uint32Setting(section.CheckIntervalSeconds),
		TargetsV4:            targetsV4,
		TargetsV6:            targetsV6,
		HTTPURLs:             append([]string(nil), section.HTTPURLs...),
	}
	return probe, nil
}

// uint8Setting narrows one optional probe setting onto its leaf's range,
// keeping an absent setting absent.
func uint8Setting(setting *int) *uint8 {
	if setting == nil {
		return nil
	}
	return new(clampUint8(*setting))
}

// uint32Setting is uint8Setting for a uint32 leaf.
func uint32Setting(setting *int) *uint32 {
	if setting == nil {
		return nil
	}
	return new(clampUint32(*setting))
}

// hashModeFromConfig reads the group's hash mode from the loaded network
// configuration, the same value the steering module acts on. A nil
// configuration, which a role-only projection passes, publishes no hash
// mode rather than a guess.
func hashModeFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	return cfg.IfMgr.HashMode
}
