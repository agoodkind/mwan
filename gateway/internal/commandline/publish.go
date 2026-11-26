package commandline

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/firewall"
	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/health"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/wanroutes"
	"goodkind.io/mwan/gateway/internal/wanconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
	"goodkind.io/mwan/gateway/internal/yangpub"
	"goodkind.io/mwan/gateway/networkjson"
)

const wanconfigPublishTimeout = 10 * time.Second

type wanconfigSurface struct {
	store     *wanstate.Store
	log       *slog.Logger
	stop      context.CancelFunc
	unobserve func()
}

// Close cancels publication before removing the state observer.
//
// Call Close after the gateway stops publishing updates.
//
// Example:
//
//	surface.Close()
func (s *wanconfigSurface) Close() {
	if s.stop != nil {
		s.stop()
	}

	if s.unobserve != nil {
		s.unobserve()
	}
}

func startWanconfigSurface(
	ctx context.Context,
	logger *slog.Logger,
	cfg *config.Config,
	moduleConfigs ifmgr.ModuleConfigSet,
	rejections []networkjson.Rejection,
	store *wanstate.Store,
) *wanconfigSurface {
	if !cfg.Wanconfig.Publish {
		return nil
	}

	log := logger.With("component", "wanconfig")
	gateway, gatewayAvailable, err := gatewayFromModuleConfigs(cfg, moduleConfigs)
	if err != nil {
		log.ErrorContext(ctx, "wanconfig: projection from module configs failed", "err", err)

		return nil
	}

	if !gatewayAvailable {
		healthConfig, configured := moduleConfigs["health"].(health.Config)
		if !configured || healthConfig.Observations == nil {
			log.InfoContext(ctx, "wanconfig: this role has no wan config to publish")

			return nil
		}
	}

	surfaceCtx, stopSurface := context.WithCancel(ctx)
	surface := &wanconfigSurface{
		store:     store,
		log:       log,
		stop:      stopSurface,
		unobserve: nil,
	}
	var notifier *surfaceNotifier
	if gatewayAvailable {
		notifier = newSurfaceNotifier(log, gateway)
		surface.unobserve = store.Observe(notifier)
	}

	go func() {
		if surface.unobserve != nil {
			defer surface.unobserve()
		}

		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(
					surfaceCtx,
					"wanconfig: initialization panicked; management surface disabled",
					"err",
					fmt.Sprint(recovered),
				)
			}
		}()
		surface.initialize(surfaceCtx, cfg, gateway, rejections, notifier)
	}()

	return surface
}

func (s *wanconfigSurface) initialize(
	ctx context.Context,
	cfg *config.Config,
	gateway wanconfig.Gateway,
	rejections []networkjson.Rejection,
	notifier *surfaceNotifier,
) {
	if ctx.Err() != nil {
		return
	}

	pub, err := yangpub.New(s.log)
	if err != nil {
		s.log.ErrorContext(
			ctx,
			"wanconfig: datastore unavailable; management surface disabled",
			"err",
			err,
		)

		return
	}

	defer func() {
		if err := pub.Close(); err != nil {
			s.log.Error("wanconfig: datastore close failed", "err", err)
		}
	}()
	if ctx.Err() != nil {
		return
	}

	if notifier == nil {
		s.initializeObservation(ctx, pub)

		return
	}

	s.initializePublished(ctx, pub, cfg, gateway, rejections, notifier)
}

func (s *wanconfigSurface) initializeObservation(
	ctx context.Context,
	pub yangpub.Publisher,
) {
	if err := registerObservationProvider(ctx, pub, s.store); err != nil {
		s.log.ErrorContext(ctx, "wanconfig: observation provider registration failed", "err", err)

		return
	}

	<-ctx.Done()
}

func (s *wanconfigSurface) initializePublished(
	ctx context.Context,
	pub yangpub.Publisher,
	cfg *config.Config,
	gateway wanconfig.Gateway,
	rejections []networkjson.Rejection,
	notifier *surfaceNotifier,
) {
	publishCtx, cancel := context.WithTimeout(ctx, wanconfigPublishTimeout)
	_ = wanconfig.Publish(publishCtx, s.log, runningReplacer{pub: pub}, gateway)
	cancel()
	if ctx.Err() != nil {
		return
	}

	ownCtx, ownCancel := context.WithTimeout(ctx, wanconfigPublishTimeout)
	ownPublishedModules(ownCtx, s.log, pub)
	ownCancel()
	if ctx.Err() != nil {
		return
	}

	senderCtx, stopSender := context.WithCancel(ctx)
	senderDone := startNotifierSender(senderCtx, s.log, notifier, pub)
	defer func() {
		stopSender()
		<-senderDone
	}()
	if err := registerLiveStateProviders(
		ctx,
		s.log,
		pub,
		s.store,
		gateway,
		rejections,
	); err != nil {
		s.log.ErrorContext(
			ctx,
			"wanconfig: provider registration failed; management surface disabled",
			"err",
			err,
		)

		return
	}

	if ctx.Err() != nil {
		return
	}

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				s.log.ErrorContext(
					ctx,
					"wanconfig: agent poller panicked; routing-session state is no longer refreshed",
					"err",
					fmt.Sprint(recovered),
				)
			}
		}()
		pollAgentBGP(ctx, s.log, cfg, s.store)
	}()
	s.log.InfoContext(
		ctx,
		"wanconfig: live-state providers registered",
		"members",
		len(gateway.Members),
	)
	<-ctx.Done()
}

func clampUint16(value int) uint16 {
	if value < 0 {
		return 0
	}

	if value > int(^uint16(0)) {
		return ^uint16(0)
	}

	return uint16(value)
}

func clampUint8(value int) uint8 {
	if value < 0 {
		return 0
	}

	if value > int(^uint8(0)) {
		return ^uint8(0)
	}

	return uint8(value)
}

// Widen values before comparing with uint32 limits on 32-bit systems.
func clampUint32(value int) uint32 {
	if value < 0 {
		return 0
	}

	if uint64(value) > uint64(^uint32(0)) {
		return ^uint32(0)
	}

	return uint32(value)
}

// Replace all owned subtrees in one sysrepo transaction.
type runningReplacer struct {
	pub yangpub.Publisher
}

func (r runningReplacer) ReplaceConfig(
	ctx context.Context,
	ownedPaths []string,
	items []wanconfig.Item,
) error {
	published := make([]yangpub.Item, 0, len(items))
	for _, item := range items {
		published = append(published, yangpub.Item{Path: item.Path, Value: item.Value})
	}

	if err := r.pub.ReplaceItems(ctx, yangpub.DatastoreRunning, ownedPaths, published); err != nil {
		slog.ErrorContext(
			ctx,
			"wanconfig: datastore replace failed",
			"items",
			len(published),
			"err",
			err,
		)

		return fmt.Errorf("replace running config: %w", err)
	}

	return nil
}

// gatewayFromModuleConfigs builds the published tree from WAN modules.
//
// The routing module provides interface data. Other modules provide probes,
// firewall, translation, and ownership settings.
//
// A health module adds probe data for WANs with health checks.
func gatewayFromModuleConfigs(
	cfg *config.Config,
	configs ifmgr.ModuleConfigSet,
) (wanconfig.Gateway, bool, error) {
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
			probed[wan.Key()] = true
		}
	}

	var pinnedConnectionID string
	var explicitConnectionIDs map[string]connectionid.ID
	if cfg != nil {
		pinnedConnectionID = cfg.IfMgr.PinnedConnectionID
		explicitConnectionIDs = cfg.IfMgr.ExplicitConnectionIDs
	}

	gateway := wanconfig.Gateway{
		InternalIface:      routesCfg.InternalIface,
		Firewall:           firewallFromConfig(cfg),
		PinnedConnectionID: pinnedConnectionID,
		ConnectionIDs:      explicitConnectionIDs,
		Connections:        connectionsFromConfig(cfg),
		HashMode:           hashModeFromConfig(cfg),
		Group:              group,
		Members:            make([]wanconfig.Member, 0, len(routesCfg.WANs)),
	}
	for _, wan := range routesCfg.WANs {
		member, err := memberFromWAN(cfg, wan, probed[wan.Key()])
		if err != nil {
			return none, false, err
		}

		gateway.Members = append(gateway.Members, member)
	}

	return gateway, true, nil
}

func memberFromWAN(
	cfg *config.Config,
	wan wanroutes.WAN,
	probed bool,
) (wanconfig.Member, error) {
	var none wanconfig.Member
	probe, err := probeSettings(cfg, wan.Key())
	if err != nil {
		return none, err
	}

	member := wanconfig.Member{
		SelectionEnabled: wan.SelectionEnabled,
		Name:             wan.Key(),
		ProviderName:     wan.Name,
		Iface:            wan.Iface,
		Tier:             wan.Tier,
		Weight:           clampUint16(wan.Weight),
		ProbePolicy:      "",
		TranslationV4:    translationV4FromConfig(cfg, wan.Key()),
		TranslationV6:    translationV6FromConfig(cfg, wan.Key()),
		TranslationIDV4:  wanconfig.TranslationInstanceID(wan.Key(), "ipv4"),
		TranslationIDV6:  wanconfig.TranslationInstanceID(wan.Key(), "ipv6"),
		TableID:          clampUint32(wan.TableID),
		FwMark:           wan.FwMark,
		FwMarkPrio:       clampUint32(wan.FwMarkPrio),
		FromPrio:         clampUint32(wan.FromPrio),
		V4Source:         wan.V4Source,
		ForcedDSCP:       forcedDSCPFromConfig(cfg, wan.Key()),
		Health:           probe,
		LinkFiles:        linkFilesFromConfig(cfg, wan.Key()),
		BGPSessions:      bgpSessionsFromConfig(cfg, wan.Key()),
	}
	if probed {
		// Health settings use the same key as the WAN member.
		member.ProbePolicy = wan.Key()
	}

	return member, nil
}

// Omit unavailable group fields from the running datastore.
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
		if group.InternalPrefix, err = parseGroupPrefix(
			"internal prefix",
			nptCfg.InternalPrefix,
		); err != nil {
			return none, err
		}

		if group.MwanbrEdgeV6, err = parseGroupAddr(
			"mwanbr edge address",
			nptCfg.MwanbrEdgeV6,
		); err != nil {
			return none, err
		}
	}

	if group.OpnsenseEdgeV6, err = parseGroupAddr(
		"router edge address",
		routesCfg.OpnsenseEdgeV6,
	); err != nil {
		return none, err
	}

	if group.InternalNetV4, err = parseGroupPrefix(
		"internal IPv4 network",
		routesCfg.InternalNetV4,
	); err != nil {
		return none, err
	}

	if healthCfg, isHealth := configs["health"].(health.Config); isHealth {
		group.ProbeTimeoutMillis = clampUint32(int(healthCfg.Timeout / time.Millisecond))
	}

	return group, nil
}

// An empty prefix leaves the leaf unset. Invalid input returns an error.
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

// Copy table reservations from the validated network configuration.
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

func forcedDSCPFromConfig(cfg *config.Config, name string) uint8 {
	if cfg == nil {
		return 0
	}

	return clampUint8(cfg.IfMgr.WAN[name].ForcedDSCP)
}

func translationV4FromConfig(cfg *config.Config, name string) *config.IPv4Translation {
	if cfg == nil {
		return nil
	}

	return cfg.IfMgr.WAN[name].TranslationV4
}

func translationV6FromConfig(cfg *config.Config, name string) *config.IPv6Translation {
	if cfg == nil {
		return nil
	}

	return cfg.IfMgr.WAN[name].TranslationV6
}

// Read link file ownership from the loaded network configuration.
func linkFilesFromConfig(cfg *config.Config, name string) string {
	if cfg == nil {
		return ""
	}

	return cfg.IfMgr.WAN[name].LinkFiles
}

func connectionsFromConfig(cfg *config.Config) []interfaceintent.Connection {
	if cfg == nil {
		return nil
	}

	return cfg.IfMgr.Connections
}

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

func uint8Setting(setting *int) *uint8 {
	if setting == nil {
		return nil
	}

	return new(clampUint8(*setting))
}

func uint32Setting(setting *int) *uint32 {
	if setting == nil {
		return nil
	}

	return new(clampUint32(*setting))
}

func hashModeFromConfig(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}

	return cfg.IfMgr.HashMode
}

func firewallFromConfig(cfg *config.Config) firewall.Config {
	if cfg == nil {
		var empty firewall.Config

		return empty
	}

	return cfg.IfMgr.Firewall
}
