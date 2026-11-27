package moduleconfig

// These routines match WAN providers to their connection intent and supply
// settings to the selected modules. Health and BGP use the shared provider
// identities on both dataplanes. OVS skips kernel steering and WAN routes.

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/links"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/steering"
	"goodkind.io/mwan/gateway/internal/ifmgr/modules/wanroutes"
)

func addWANRoleConfigs(
	moduleConfigs ifmgr.ModuleConfigSet,
	want map[string]bool,
	ifmgrCfg config.IfMgrSection,
) error {
	shared := WANRefs(ifmgrCfg)
	var routesSection *config.IfMgrWANRoutesSection
	if ifmgrCfg.Modules.WAN != nil {
		routesSection = ifmgrCfg.Modules.WAN.Routes
	}

	if want["links"] {
		linksConfig := links.Config{Connections: ifmgrCfg.Connections, StateFile: ""}
		if ifmgrCfg.Modules.Links != nil {
			linksConfig.StateFile = ifmgrCfg.Modules.Links.StateFile
		}

		moduleConfigs["links"] = linksConfig
	}

	if want["autoconfiguration"] {
		moduleConfigs["autoconfiguration"] = buildAutoconfigurationConfig(ifmgrCfg)
	}

	if want["resolver"] {
		moduleConfigs["resolver"] = buildResolverConfig(ifmgrCfg)
	}

	if want["addresses"] {
		addressesConfig, err := buildAddressesConfig(ifmgrCfg)
		if err != nil {
			return err
		}

		moduleConfigs["addresses"] = addressesConfig
	}

	if want["health"] {
		healthConfig, err := HealthConfig(shared, ifmgrCfg.Modules.Health)
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

	if want["bgp_sessions"] {
		moduleConfigs["bgp_sessions"] = buildBGPSessionsConfig(
			shared,
			ifmgrCfg.Modules.BGPSessions,
			ifmgrCfg.Connections,
			ifmgrCfg.OVSDataplane,
		)
	}

	if want["firewall"] {
		moduleConfigs["firewall"] = ifmgrCfg.Firewall
	}

	return nil
}

func buildSteeringConfig(
	shared WANInputs,
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

		cfg.Members = append(
			cfg.Members,
			steering.Member{
				WANRef:           wan.WANRef,
				Mark:             mark,
				Tier:             wan.Tier,
				Weight:           wan.Weight,
				SelectionEnabled: wan.SelectionEnabled,
			},
		)
	}

	return cfg, nil
}

func buildNPTConfig(shared WANInputs) npt.Config {
	return npt.Config{
		InternalIface:  shared.InternalIface,
		InternalNetV4:  shared.InternalNetV4,
		InternalPrefix: shared.InternalPrefix,
		OpnsenseEdgeV6: shared.OpnsenseEdgeV6,
		MwanbrEdgeV6:   shared.MwanbrEdgeV6,
		WANs:           shared.nptWANs(),
	}
}

type sharedWAN struct {
	ifmgr.WANRef

	SelectionEnabled *bool
	Owned            bool
	TableID          int
	FwMark           int
	FwMarkPrio       int
	FromPrio         int
	TranslationV4    *config.IPv4Translation
	TranslationV6    *config.IPv6Translation
	V4Source         string
	Tier             uint8
	Weight           int
	Tunnel           *interfaceintent.Tunnel
	BGPSessions      []config.BGPSession
}

// WANInputs contains the settings common to WAN routing, health, and steering.
//
// WANRefs builds these inputs from the configured providers and connections.
//
//	shared := moduleconfig.WANRefs(settings.IfMgr)
type WANInputs struct {
	InternalIface  string
	InternalNetV4  string
	WANs           []sharedWAN
	InternalPrefix string
	OpnsenseEdgeV6 string
	MwanbrEdgeV6   string
}

func (s WANInputs) nptWANs() []npt.WAN {
	wans := make([]npt.WAN, 0, len(s.WANs))
	for _, wan := range s.WANs {
		wans = append(
			wans,
			npt.WAN{
				WANRef:        wan.WANRef,
				Owned:         wan.Owned,
				TranslationV4: wan.TranslationV4,
				Translation:   wan.TranslationV6,
			},
		)
	}

	return wans
}

// WANRefs returns providers in connection ID order for the role modules.
//
// It matches each provider to its connection for link and ownership settings.
//
//	shared := moduleconfig.WANRefs(settings.IfMgr)
func WANRefs(ifmgrCfg config.IfMgrSection) WANInputs {
	inputs := WANInputs{
		InternalIface:  "",
		InternalNetV4:  "",
		WANs:           make([]sharedWAN, 0, len(ifmgrCfg.WAN)),
		InternalPrefix: ifmgrCfg.InternalPrefix,
		OpnsenseEdgeV6: ifmgrCfg.OpnsenseEdgeV6,
		MwanbrEdgeV6:   ifmgrCfg.MwanbrEdgeV6,
	}
	if ifmgrCfg.Modules.WAN != nil && ifmgrCfg.Modules.WAN.Routes != nil {
		inputs.InternalIface = ifmgrCfg.Modules.WAN.Routes.InternalIface
		inputs.InternalNetV4 = ifmgrCfg.Modules.WAN.Routes.InternalNetV4
	}

	names := make([]string, 0, len(ifmgrCfg.WAN))
	for name := range ifmgrCfg.WAN {
		names = append(names, name)
	}

	sort.Strings(names)
	connections := make(map[string]interfaceintent.Connection, len(ifmgrCfg.Connections))

	for _, connection := range ifmgrCfg.Connections {
		connections[connection.ID.String()] = connection
	}

	for _, name := range names {
		entry := ifmgrCfg.WAN[name]
		providerName := entry.ProviderName
		if providerName == "" {
			providerName = name
		}

		ref := ifmgr.WANRef{ID: connectionid.ID(name), Name: providerName, Iface: entry.Iface}
		connection := connections[name]
		var tunnel *interfaceintent.Tunnel
		if connection.Link != nil {
			tunnel = connection.Link.Tunnel
		}

		inputs.WANs = append(
			inputs.WANs,
			sharedWAN{
				WANRef:           ref,
				Owned:            connection.Owner == interfaceintent.OwnerMWAN,
				TableID:          entry.TableID,
				FwMark:           entry.FwMark,
				FwMarkPrio:       entry.FwMarkPrio,
				FromPrio:         entry.FromPrio,
				TranslationV4:    entry.TranslationV4,
				TranslationV6:    entry.TranslationV6,
				V4Source:         entry.V4Source,
				Tier:             entry.Tier,
				Weight:           entry.Weight,
				SelectionEnabled: entry.SelectionEnabled,
				Tunnel:           tunnel,
				BGPSessions:      entry.BGPSessions,
			},
		)
	}

	return inputs
}

func buildWANRoutesConfig(
	shared WANInputs,
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

		cfg.WANs = append(
			cfg.WANs,
			wanroutes.WAN{
				WANRef:               wan.WANRef,
				Owned:                wan.Owned,
				TableID:              wan.TableID,
				FwMark:               mark,
				FwMarkPrio:           wan.FwMarkPrio,
				FromPrio:             wan.FromPrio,
				TranslationV4:        wan.TranslationV4,
				TranslationV6:        wan.TranslationV6,
				V4Source:             wan.V4Source,
				Tier:                 wan.Tier,
				Weight:               wan.Weight,
				SelectionEnabled:     wan.SelectionEnabled,
				MappedExternals:      mappedExternals(wan.TranslationV4),
				LocalMappedExternals: localMappedExternals(wan.TranslationV4),
				Tunnel:               wan.Tunnel,
				BGPRouteMetrics:      bgpRouteMetrics(wan.BGPSessions),
			},
		)
	}

	return cfg, nil
}

var (
	errNegativeMark = errors.New("fw-mark must be >= 0")
	errMarkOverflow = errors.New("exceeds uint32")
)

func wanFwMark(wan sharedWAN) (uint32, error) {
	if wan.FwMark < 0 {
		slog.Warn("WAN firewall mark is negative.", "wan", wan.Name, "mark", wan.FwMark)

		return 0, fmt.Errorf("network.json wan %s %w", wan.Name, errNegativeMark)
	}

	if uint64(wan.FwMark) > uint64(^uint32(0)) {
		slog.Warn("WAN firewall mark exceeds uint32.", "wan", wan.Name, "mark", wan.FwMark)

		return 0, fmt.Errorf(
			"network.json wan %s fw-mark %d %w",
			wan.Name,
			wan.FwMark,
			errMarkOverflow,
		)
	}

	return uint32(wan.FwMark), nil
}
