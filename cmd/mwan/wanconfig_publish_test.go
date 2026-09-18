package main

import (
	"net/netip"
	"reflect"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/health"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/ifmgr/modules/wanroutes"
	"goodkind.io/mwan/internal/wanconfig"
)

// wanconfigTestModuleConfigs mirrors what buildIfMgrModuleConfigs produces
// for the wan role from the rendered testbed config: three WANs on
// wan.routes, health probing all three, and the shared internal prefix on
// npt.
func wanconfigTestModuleConfigs() ifmgr.ModuleConfigSet {
	wans := []wanroutes.WAN{
		{
			WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0.3242"}, TableID: 100,
			FwMark: 1, FwMarkPrio: 10, FromPrio: 20, NptPrefix: "2001:db8:a::/60", V4Source: "",
			Tier: 0, Weight: 1,
		},
		{
			WANRef: ifmgr.WANRef{Name: "monkeybrains", Iface: "enmbrains0"}, TableID: 300,
			FwMark: 3, FwMarkPrio: 12, FromPrio: 22, NptPrefix: "", V4Source: "",
			Tier: 1, Weight: 1,
		},
		{
			WANRef: ifmgr.WANRef{Name: "webpass", Iface: "enwebpass0"}, TableID: 200,
			FwMark: 2, FwMarkPrio: 11, FromPrio: 21, NptPrefix: "2001:db8:b::/60", V4Source: "192.0.2.2",
			Tier: 0, Weight: 2,
		},
	}
	routesCfg := wanroutes.Config{
		InternalIface:   "eninternal0",
		OpnsenseEdgeV6:  "2001:db8:fe::2",
		InternalNetV4:   "192.0.2.0/29",
		HealthStateFile: "/run/health",
		WANs:            wans,
	}
	healthCfg := health.Config{}
	healthCfg.Timeout = 2 * time.Second
	for _, wan := range wans {
		probedWAN := health.WAN{}
		probedWAN.WANRef = wan.WANRef
		healthCfg.WANs = append(healthCfg.WANs, probedWAN)
	}
	nptCfg := npt.Config{
		InternalPrefix: "3d06:bad:b01:210::/60",
		OpnsenseEdgeV6: "2001:db8:fe::2",
		MwanbrEdgeV6:   "2001:db8:fe::3",
		WANs:           []npt.WAN{},
	}
	return ifmgr.ModuleConfigSet{
		"wan.routes": routesCfg,
		"npt":        nptCfg,
		"health":     healthCfg,
	}
}

// TestGatewayFromModuleConfigs_ProjectsTheWANRole pins the projection the
// daemon publishes: every WAN becomes a member with the tier and routing
// numbers the configuration assigns, a probe policy named after it, and the
// translation pair joined from the npt internal prefix and its own external
// prefix; and the group carries the values the module configs and the loaded
// configuration hold.
func TestGatewayFromModuleConfigs_ProjectsTheWANRole(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.IfMgr.HashMode = "source"
	cfg.IfMgr.ReservedTables = []int{400, 500}
	gateway, ok, err := gatewayFromModuleConfigs(cfg, wanconfigTestModuleConfigs())
	if err != nil {
		t.Fatalf("gatewayFromModuleConfigs: %v", err)
	}
	if !ok {
		t.Fatal("ok = false, want a gateway")
	}
	if gateway.InternalIface != "eninternal0" {
		t.Fatalf("InternalIface = %q", gateway.InternalIface)
	}
	if gateway.HashMode != "source" {
		t.Fatalf("HashMode = %q, want source", gateway.HashMode)
	}

	internal := netip.MustParsePrefix("3d06:bad:b01:210::/60")
	wantGroup := wanconfig.GroupSettings{
		ReservedTables:     []uint32{400, 500},
		InternalPrefix:     internal,
		OpnsenseEdgeV6:     netip.MustParseAddr("2001:db8:fe::2"),
		MwanbrEdgeV6:       netip.MustParseAddr("2001:db8:fe::3"),
		InternalNetV4:      netip.MustParsePrefix("192.0.2.0/29"),
		ProbeTimeoutMillis: 2000,
	}
	if !reflect.DeepEqual(gateway.Group, wantGroup) {
		t.Fatalf("group = %+v, want %+v", gateway.Group, wantGroup)
	}

	want := []wanconfig.Member{
		{
			Name: "att", Iface: "enatt0.3242", Tier: 0, Weight: 1, ProbePolicy: "att",
			NPTInternal: internal, NPTExternal: netip.MustParsePrefix("2001:db8:a::/60"),
			TableID: 100, FwMark: 1, FwMarkPrio: 10, FromPrio: 20,
		},
		{
			Name: "monkeybrains", Iface: "enmbrains0", Tier: 1, Weight: 1, ProbePolicy: "monkeybrains",
			TableID: 300, FwMark: 3, FwMarkPrio: 12, FromPrio: 22,
		},
		{
			Name: "webpass", Iface: "enwebpass0", Tier: 0, Weight: 2, ProbePolicy: "webpass",
			NPTInternal: internal, NPTExternal: netip.MustParsePrefix("2001:db8:b::/60"),
			TableID: 200, FwMark: 2, FwMarkPrio: 11, FromPrio: 21, V4Source: "192.0.2.2",
		},
	}
	if !reflect.DeepEqual(gateway.Members, want) {
		t.Fatalf("members = %+v, want %+v", gateway.Members, want)
	}

	// The projection must feed the tree builder without further shaping.
	if _, err := wanconfig.ConfigItems(gateway); err != nil {
		t.Fatalf("ConfigItems on the projected gateway: %v", err)
	}
}

// TestGatewayFromModuleConfigs_CarriesWhatOnlyTheLoadedConfigHolds pins the
// values no module config carries: a forced DSCP value, both halves of each
// static mapping, an enabled probe's settings, a disabled probe with exactly
// the settings its file carried, and no probe at all for a provider with no
// health container.
func TestGatewayFromModuleConfigs_CarriesWhatOnlyTheLoadedConfigHolds(t *testing.T) {
	t.Parallel()
	mappings := []config.StaticMapping{
		{External: netip.MustParseAddr("198.51.100.2"), Internal: netip.MustParseAddr("10.250.250.2")},
		{External: netip.MustParseAddr("198.51.100.3"), Internal: netip.MustParseAddr("10.250.250.3")},
	}
	cfg := &config.Config{}
	cfg.IfMgr.WAN = map[string]config.IfMgrWANEntry{"att": {ForcedDSCP: 8, StaticMappings: mappings}}
	cfg.IfMgr.Modules.Health = &config.IfMgrHealthSection{WAN: map[string]config.IfMgrHealthWANSection{
		"att": {
			Enabled:              true,
			PingCount:            new(3),
			SuccessThreshold:     new(2),
			CheckIntervalSeconds: new(10),
			FailureThreshold:     new(2),
			RecoveryThreshold:    new(2),
			TargetsV4:            []string{"192.0.2.10"},
			TargetsV6:            []string{"2001:db8:53::1"},
			HTTPURLs:             []string{"https://example.test/ip"},
		},
		"monkeybrains": {Enabled: false, PingCount: new(4), CheckIntervalSeconds: new(0)},
	}}

	gateway, ok, err := gatewayFromModuleConfigs(cfg, wanconfigTestModuleConfigs())
	if err != nil || !ok {
		t.Fatalf("gatewayFromModuleConfigs: ok=%v err=%v", ok, err)
	}
	byName := map[string]wanconfig.Member{}
	for _, member := range gateway.Members {
		byName[member.Name] = member
	}

	att := byName["att"]
	if att.ForcedDSCP != 8 {
		t.Fatalf("att forced DSCP = %d, want 8", att.ForcedDSCP)
	}
	wantMappings := []wanconfig.StaticMapping{
		{External: netip.MustParseAddr("198.51.100.2"), Internal: netip.MustParseAddr("10.250.250.2")},
		{External: netip.MustParseAddr("198.51.100.3"), Internal: netip.MustParseAddr("10.250.250.3")},
	}
	if !reflect.DeepEqual(att.StaticMappings, wantMappings) {
		t.Fatalf("att static mappings = %+v, want %+v in configuration order", att.StaticMappings, wantMappings)
	}
	wantAttProbe := &wanconfig.ProbeSettings{
		Enabled:              true,
		PingCount:            new(uint8(3)),
		SuccessThreshold:     new(uint8(2)),
		FailureThreshold:     new(uint8(2)),
		RecoveryThreshold:    new(uint8(2)),
		CheckIntervalSeconds: new(uint32(10)),
		TargetsV4:            []netip.Addr{netip.MustParseAddr("192.0.2.10")},
		TargetsV6:            []netip.Addr{netip.MustParseAddr("2001:db8:53::1")},
		HTTPURLs:             []string{"https://example.test/ip"},
	}
	if !reflect.DeepEqual(att.Health, wantAttProbe) {
		t.Fatalf("att probe = %+v, want %+v", att.Health, wantAttProbe)
	}

	// The whole probe is compared, so any setting the loaded section did not
	// carry fails here. The address lists are empty rather than nil because
	// the projection parses them into fresh slices.
	wantDisabledProbe := &wanconfig.ProbeSettings{
		Enabled:              false,
		PingCount:            new(uint8(4)),
		SuccessThreshold:     nil,
		FailureThreshold:     nil,
		RecoveryThreshold:    nil,
		CheckIntervalSeconds: new(uint32(0)),
		TargetsV4:            []netip.Addr{},
		TargetsV6:            []netip.Addr{},
		HTTPURLs:             nil,
	}
	if disabled := byName["monkeybrains"].Health; !reflect.DeepEqual(disabled, wantDisabledProbe) {
		t.Fatalf("monkeybrains probe = %+v, want exactly %+v", disabled, wantDisabledProbe)
	}

	webpass := byName["webpass"]
	if webpass.Health != nil || webpass.ForcedDSCP != 0 || webpass.StaticMappings != nil {
		t.Fatalf("webpass = %+v, want no probe, no forced DSCP, and no static mappings", webpass)
	}

	if _, err := wanconfig.ConfigItems(gateway); err != nil {
		t.Fatalf("ConfigItems on the projected gateway: %v", err)
	}
}

// TestGatewayFromModuleConfigs_QuietOutsideTheWANRole pins that a role with
// no wan.routes config (every role but wan) yields no gateway and no error,
// so enabling the publish gate on such a host is a logged no-op.
func TestGatewayFromModuleConfigs_QuietOutsideTheWANRole(t *testing.T) {
	t.Parallel()
	_, ok, err := gatewayFromModuleConfigs(nil, ifmgr.ModuleConfigSet{})
	if err != nil {
		t.Fatalf("gatewayFromModuleConfigs: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want no gateway")
	}
}

// TestGatewayFromModuleConfigs_UnprobedMemberHasNoPolicy pins that a WAN
// absent from the health config gets no probe-policy value.
func TestGatewayFromModuleConfigs_UnprobedMemberHasNoPolicy(t *testing.T) {
	t.Parallel()
	configs := wanconfigTestModuleConfigs()
	configs["health"] = health.Config{}

	gateway, ok, err := gatewayFromModuleConfigs(nil, configs)
	if err != nil || !ok {
		t.Fatalf("gatewayFromModuleConfigs: ok=%v err=%v", ok, err)
	}
	for _, member := range gateway.Members {
		if member.ProbePolicy != "" {
			t.Fatalf("member %s ProbePolicy = %q, want empty", member.Name, member.ProbePolicy)
		}
	}
}

// TestGatewayFromModuleConfigs_RejectsAnUnparsableTranslationPrefix pins
// the loud failure: a malformed prefix in the loaded config returns an
// error rather than a member silently missing its translation instance.
func TestGatewayFromModuleConfigs_RejectsAnUnparsableTranslationPrefix(t *testing.T) {
	t.Parallel()
	configs := wanconfigTestModuleConfigs()
	routesCfg, isRoutes := configs["wan.routes"].(wanroutes.Config)
	if !isRoutes {
		t.Fatal("wan.routes config missing from fixture")
	}
	routesCfg.WANs[0].NptPrefix = "not-a-prefix"
	configs["wan.routes"] = routesCfg

	_, _, err := gatewayFromModuleConfigs(nil, configs)
	if err == nil {
		t.Fatal("err = nil, want a parse error")
	}
}

// TestGatewayFromModuleConfigs_RejectsAnUnparsableNetworkValue pins the same
// loud failure for every other value held as text: a group address or prefix,
// and a probe target, each fails the projection rather than leaving its leaf
// silently unpublished.
func TestGatewayFromModuleConfigs_RejectsAnUnparsableNetworkValue(t *testing.T) {
	t.Parallel()
	cases := map[string]func(cfg *config.Config, configs ifmgr.ModuleConfigSet){
		"internal IPv4 network": func(_ *config.Config, configs ifmgr.ModuleConfigSet) {
			routesCfg, _ := configs["wan.routes"].(wanroutes.Config)
			routesCfg.InternalNetV4 = "not-a-prefix"
			configs["wan.routes"] = routesCfg
		},
		"router edge address": func(_ *config.Config, configs ifmgr.ModuleConfigSet) {
			routesCfg, _ := configs["wan.routes"].(wanroutes.Config)
			routesCfg.OpnsenseEdgeV6 = "not-an-address"
			configs["wan.routes"] = routesCfg
		},
		"gateway edge address": func(_ *config.Config, configs ifmgr.ModuleConfigSet) {
			nptCfg, _ := configs["npt"].(npt.Config)
			nptCfg.MwanbrEdgeV6 = "not-an-address"
			configs["npt"] = nptCfg
		},
		"disabled probe target": func(cfg *config.Config, _ ifmgr.ModuleConfigSet) {
			cfg.IfMgr.Modules.Health = &config.IfMgrHealthSection{WAN: map[string]config.IfMgrHealthWANSection{
				"att": {Enabled: false, TargetsV4: []string{"not-an-address"}},
			}}
		},
	}
	for name, breakValue := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{}
			configs := wanconfigTestModuleConfigs()
			breakValue(cfg, configs)
			if _, _, err := gatewayFromModuleConfigs(cfg, configs); err == nil {
				t.Fatal("err = nil, want a parse error")
			}
		})
	}
	if _, _, err := gatewayFromModuleConfigs(&config.Config{}, wanconfigTestModuleConfigs()); err != nil {
		t.Fatalf("the unbroken fixture every case starts from is rejected: %v", err)
	}
}
