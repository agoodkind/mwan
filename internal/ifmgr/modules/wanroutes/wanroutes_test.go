package wanroutes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/wanstate"
)

func TestDesiredState(t *testing.T) {
	t.Parallel()

	baseConfig := testConfig()
	baseGateways := testGateways()
	allHealthy := netif.HealthStates{
		"att":          netif.HealthStateHealthy,
		"webpass":      netif.HealthStateHealthy,
		"monkeybrains": netif.HealthStateHealthy,
	}
	noneHealthy := netif.HealthStates{
		"att":          netif.HealthStateUnhealthy,
		"webpass":      netif.HealthStateUnhealthy,
		"monkeybrains": netif.HealthStateUnhealthy,
	}

	cases := []struct {
		name       string
		cfg        Config
		gateways   gateways
		health     netif.HealthStates
		wantRules  []netif.DesiredRule
		wantRoutes []netif.RouteSpec
	}{
		{
			name:       "every provider healthy with gateways",
			cfg:        baseConfig,
			gateways:   baseGateways,
			health:     allHealthy,
			wantRules:  allHealthyRules(baseConfig),
			wantRoutes: routesForGateways(baseConfig, baseGateways),
		},
		{
			// No verdict has been recorded yet, so every provider reads healthy
			// and the first tier activates. This is the startup pass.
			name:       "no verdict recorded activates the first tier",
			cfg:        baseConfig,
			gateways:   baseGateways,
			health:     netif.HealthStates{},
			wantRules:  allHealthyRules(baseConfig),
			wantRoutes: routesForGateways(baseConfig, baseGateways),
		},
		{
			name: "unhealthy provider and missing gateway drop enabled rules",
			cfg:  baseConfig,
			gateways: gateways{
				"att":          baseGateways["att"],
				"webpass":      baseGateways["webpass"],
				"monkeybrains": {V4: "198.51.100.1", V6: ""},
			},
			health: netif.HealthStates{
				"att":          netif.HealthStateHealthy,
				"webpass":      netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			wantRules: []netif.DesiredRule{
				fwmarkRule(familyV4, 100, 1, 100),
				fwmarkRule(familyV6, 100, 1, 100),
				fromRule(55, "3d06:bad:b01:1100::/56", 100),
				fwmarkRule(familyV4, 300, 3, 300),
			},
			wantRoutes: routesForGateways(baseConfig, gateways{
				"att":          baseGateways["att"],
				"webpass":      baseGateways["webpass"],
				"monkeybrains": {V4: "198.51.100.1", V6: ""},
			}),
		},
		{
			// The active tier is not the first configured tier and exactly one
			// provider in it is healthy, so nothing else marks internal traffic
			// and the catch-all pair carries it. This reproduces today's
			// behavior with Monkeybrains alone in the fallback tier.
			name:     "a lone healthy provider below the first tier gets the catch-all",
			cfg:      baseConfig,
			gateways: baseGateways,
			health: netif.HealthStates{
				"att":          netif.HealthStateUnhealthy,
				"webpass":      netif.HealthStateUnhealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			wantRules: []netif.DesiredRule{
				fwmarkRule(familyV4, 300, 3, 300),
				fwmarkRule(familyV6, 300, 3, 300),
				fromRule(57, "3d06:bad:b01:3300::/56", 300),
				catchAllRule(familyV4, "vmbr250", 300),
				catchAllRule(familyV6, "vmbr250", 300),
			},
			wantRoutes: routesForGateways(baseConfig, baseGateways),
		},
		{
			// Two healthy providers share the active tier, so the steering
			// module's marks carry the split. A catch-all would send every
			// unmarked packet to one of them and undo it.
			name:     "two healthy providers in the active tier get no catch-all",
			cfg:      configWithWebpassInTierOne(baseConfig),
			gateways: baseGateways,
			health: netif.HealthStates{
				"att":          netif.HealthStateUnhealthy,
				"webpass":      netif.HealthStateHealthy,
				"monkeybrains": netif.HealthStateHealthy,
			},
			wantRules: []netif.DesiredRule{
				fwmarkRule(familyV4, 200, 2, 200),
				fromRuleV4(56, "203.0.113.2", 200),
				fwmarkRule(familyV6, 200, 2, 200),
				fromRule(56, "3d06:bad:b01:2200::/56", 200),
				fwmarkRule(familyV4, 300, 3, 300),
				fwmarkRule(familyV6, 300, 3, 300),
				fromRule(57, "3d06:bad:b01:3300::/56", 300),
			},
			wantRoutes: routesForGateways(configWithWebpassInTierOne(baseConfig), baseGateways),
		},
		{
			// Nothing is healthy, so no tier is active and no catch-all is
			// installed. Sending traffic at a provider that failed its probes
			// would be worse than letting it fall to the main table.
			name:       "no healthy provider installs no catch-all",
			cfg:        baseConfig,
			gateways:   baseGateways,
			health:     noneHealthy,
			wantRules:  []netif.DesiredRule{},
			wantRoutes: routesForGateways(baseConfig, baseGateways),
		},
		{
			name:     "from-PD rule requires NPT prefix",
			cfg:      configWithoutWebpassNPT(baseConfig),
			gateways: baseGateways,
			health:   allHealthy,
			wantRules: []netif.DesiredRule{
				fwmarkRule(familyV4, 100, 1, 100),
				fwmarkRule(familyV6, 100, 1, 100),
				fromRule(55, "3d06:bad:b01:1100::/56", 100),
				fwmarkRule(familyV4, 200, 2, 200),
				fromRuleV4(56, "203.0.113.2", 200),
				fwmarkRule(familyV6, 200, 2, 200),
				fwmarkRule(familyV4, 300, 3, 300),
				fwmarkRule(familyV6, 300, 3, 300),
				fromRule(57, "3d06:bad:b01:3300::/56", 300),
			},
			wantRoutes: routesForGateways(configWithoutWebpassNPT(baseConfig), baseGateways),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gotRules, gotRoutes := desiredState(tc.gateways, tc.health, tc.cfg)
			if !reflect.DeepEqual(gotRules, tc.wantRules) {
				t.Fatalf("rules mismatch\ngot:  %#v\nwant: %#v", gotRules, tc.wantRules)
			}
			if !reflect.DeepEqual(gotRoutes, tc.wantRoutes) {
				t.Fatalf("routes mismatch\ngot:  %#v\nwant: %#v", gotRoutes, tc.wantRoutes)
			}
		})
	}
}

// TestPublishLiveStateReportsTheActiveTier pins what the management surface
// serves: the active tier the pass decided, and one carrying flag per provider
// that is true only for a provider in that tier which is healthy and has a
// gateway.
func TestPublishLiveStateReportsTheActiveTier(t *testing.T) {
	t.Parallel()

	store := wanstate.New()
	module := &Module{cfg: testConfig()}
	module.InitBase(testEnvWithStore(store), "module", moduleName)

	module.publishLiveState(testGateways(), netif.HealthStates{
		"att":          netif.HealthStateUnhealthy,
		"webpass":      netif.HealthStateUnhealthy,
		"monkeybrains": netif.HealthStateHealthy,
	})

	snapshot := store.Snapshot()
	if !snapshot.TierValid || snapshot.ActiveTier != 1 {
		t.Fatalf("active tier = %d (valid=%v), want 1", snapshot.ActiveTier, snapshot.TierValid)
	}
	want := map[string]bool{"att": false, "webpass": false, "monkeybrains": true}
	for name, wantCarrying := range want {
		routing, ok := snapshot.Routing[name]
		if !ok {
			t.Fatalf("%s missing from snapshot.Routing", name)
		}
		if routing.Carrying != wantCarrying {
			t.Fatalf("%s carrying = %v, want %v", name, routing.Carrying, wantCarrying)
		}
	}
}

// TestPublishLiveStateWithNoHealthyProvider pins that a pass in which nothing is
// healthy carries nobody, rather than reporting the first tier as if it were
// serving traffic.
func TestPublishLiveStateWithNoHealthyProvider(t *testing.T) {
	t.Parallel()

	store := wanstate.New()
	module := &Module{cfg: testConfig()}
	module.InitBase(testEnvWithStore(store), "module", moduleName)

	module.publishLiveState(testGateways(), netif.HealthStates{
		"att":          netif.HealthStateUnhealthy,
		"webpass":      netif.HealthStateUnhealthy,
		"monkeybrains": netif.HealthStateUnhealthy,
	})

	for name, routing := range store.Snapshot().Routing {
		if routing.Carrying {
			t.Fatalf("%s reported carrying with no healthy provider", name)
		}
	}
}

// TestValidateWANAcceptsAnyPositivePriority pins that the two fixed priority
// checks are gone: a fourth provider's numbers are admitted, and only a
// non-positive value is refused.
func TestValidateWANAcceptsAnyPositivePriority(t *testing.T) {
	t.Parallel()

	fourth := WAN{
		WANRef:     ifmgr.WANRef{Name: "astount", Iface: "astount0"},
		TableID:    600,
		FwMark:     4,
		FwMarkPrio: 600,
		FromPrio:   58,
		NptPrefix:  "3d06:bad:b01:2500::/60",
		V4Source:   "",
		Tier:       2,
		Weight:     1,
	}
	if err := validateWAN(fourth); err != nil {
		t.Fatalf("validateWAN rejected a fourth provider: %v", err)
	}
	fourth.FwMarkPrio = 0
	if err := validateWAN(fourth); err == nil {
		t.Fatal("validateWAN accepted a zero fw_mark_prio")
	}
	fourth.FwMarkPrio = 600
	fourth.FromPrio = 0
	if err := validateWAN(fourth); err == nil {
		t.Fatal("validateWAN accepted a zero from_prio")
	}
	fourth.FromPrio = 58
	fourth.FwMarkPrio = catchAllPriority
	if err := validateWAN(fourth); err == nil || !strings.Contains(err.Error(), "catch-all") {
		t.Fatalf("validateWAN(FwMarkPrio=catchAllPriority) = %v, want an error mentioning the catch-all priority", err)
	}
	fourth.FwMarkPrio = 600
	fourth.FromPrio = catchAllPriority
	if err := validateWAN(fourth); err == nil || !strings.Contains(err.Error(), "catch-all") {
		t.Fatalf("validateWAN(FromPrio=catchAllPriority) = %v, want an error mentioning the catch-all priority", err)
	}
}

func TestInitReturnsDisabledSentinelWhenWANsEmpty(t *testing.T) {
	t.Parallel()

	module, err := New(Config{})
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	initErr := module.Init(context.Background(), testEnv())
	if initErr == nil {
		t.Fatal("Init returned nil error for empty WANs, want ErrModuleDisabled")
	}
	if !errors.Is(initErr, ifmgr.ErrModuleDisabled) {
		t.Fatalf("Init returned err=%v, want errors.Is(err, ifmgr.ErrModuleDisabled)", initErr)
	}
}

func TestDesiredStateOmitsStaticInternalRoute(t *testing.T) {
	t.Parallel()

	gateways := testGateways()
	health := netif.HealthStates{
		"att":          netif.HealthStateHealthy,
		"webpass":      netif.HealthStateHealthy,
		"monkeybrains": netif.HealthStateHealthy,
	}
	cfg := testConfig()

	_, got := desiredState(gateways, health, cfg)

	want := routesForGateways(cfg, gateways)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
	for _, gotRoute := range got {
		if gotRoute.Via == cfg.OpnsenseEdgeV6 {
			t.Fatalf("static internal route via the edge address remains: %#v", gotRoute)
		}
	}
	for _, wan := range cfg.WANs {
		wantTransit := route(familyV4, cfg.InternalNetV4, "", cfg.InternalIface, wan.TableID, 0)
		wantEdge := route(familyV6, withPrefix(cfg.OpnsenseEdgeV6, "128"), "", cfg.InternalIface, wan.TableID, 0)
		if !containsRoute(got, wantTransit) {
			t.Fatalf("missing transit route: %#v", wantTransit)
		}
		if !containsRoute(got, wantEdge) {
			t.Fatalf("missing edge route: %#v", wantEdge)
		}
	}
}

func testConfig() Config {
	return Config{
		InternalIface:   "vmbr250",
		OpnsenseEdgeV6:  "3d06:bad:b01:201::1",
		InternalNetV4:   "10.250.250.0/29",
		HealthStateFile: "/run/mwan-health.state",
		WANs: []WAN{
			{
				WANRef:     ifmgr.WANRef{Name: "att", Iface: "att0"},
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:1100::/56",
				V4Source:   "",
				Tier:       0,
				Weight:     1,
			},
			{
				WANRef:     ifmgr.WANRef{Name: "webpass", Iface: "webpass0"},
				TableID:    200,
				FwMark:     2,
				FwMarkPrio: 200,
				FromPrio:   56,
				NptPrefix:  "3d06:bad:b01:2200::/56",
				V4Source:   "203.0.113.2",
				Tier:       0,
				Weight:     1,
			},
			{
				WANRef:     ifmgr.WANRef{Name: "monkeybrains", Iface: "mbrains0"},
				TableID:    300,
				FwMark:     3,
				FwMarkPrio: 300,
				FromPrio:   57,
				NptPrefix:  "3d06:bad:b01:3300::/56",
				V4Source:   "",
				Tier:       1,
				Weight:     1,
			},
		},
	}
}

func testGateways() gateways {
	return gateways{
		"att":          {V4: "192.0.2.1", V6: "fe80::a"},
		"webpass":      {V4: "203.0.113.1", V6: "fe80::b"},
		"monkeybrains": {V4: "198.51.100.1", V6: "fe80::c"},
	}
}

func allHealthyRules(cfg Config) []netif.DesiredRule {
	return []netif.DesiredRule{
		fwmarkRule(familyV4, 100, 1, 100),
		fwmarkRule(familyV6, 100, 1, 100),
		fromRule(55, cfg.WANs[0].NptPrefix, 100),
		fwmarkRule(familyV4, 200, 2, 200),
		fromRuleV4(56, cfg.WANs[1].V4Source, 200),
		fwmarkRule(familyV6, 200, 2, 200),
		fromRule(56, cfg.WANs[1].NptPrefix, 200),
		fwmarkRule(familyV4, 300, 3, 300),
		fwmarkRule(familyV6, 300, 3, 300),
		fromRule(57, cfg.WANs[2].NptPrefix, 300),
	}
}

func routesForGateways(cfg Config, currentGateways gateways) []netif.RouteSpec {
	routes := make([]netif.RouteSpec, 0, len(cfg.WANs)*5+1)
	for _, wan := range cfg.WANs {
		wanGateways := currentGateways[wan.Name]
		if wanGateways.V4 != "" {
			routes = append(routes, route(familyV4, "default", wanGateways.V4, wan.Iface, wan.TableID, 0))
		}
		if wanGateways.V6 != "" {
			routes = append(routes, route(familyV6, "default", wanGateways.V6, wan.Iface, wan.TableID, 0))
		}
		routes = append(routes,
			route(familyV4, cfg.InternalNetV4, "", cfg.InternalIface, wan.TableID, 0),
			route(familyV6, withPrefix(cfg.OpnsenseEdgeV6, "128"), "", cfg.InternalIface, wan.TableID, 0),
		)
	}
	return routes
}

func containsRoute(routes []netif.RouteSpec, want netif.RouteSpec) bool {
	for _, got := range routes {
		if got == want {
			return true
		}
	}
	return false
}

func route(family string, dest string, via string, dev string, tableID int, metric int) netif.RouteSpec {
	return netif.RouteSpec{
		Family:   family,
		Dest:     dest,
		Via:      via,
		Dev:      dev,
		TableID:  tableID,
		Metric:   metric,
		Protocol: 0,
	}
}

func fwmarkRule(family string, priority int, mark uint32, tableID int) netif.DesiredRule {
	return netif.DesiredRule{
		Family:   family,
		Priority: priority,
		From:     "",
		Mark:     mark,
		IifName:  "",
		UIDRange: "",
		Table:    "",
		TableID:  tableID,
	}
}

func fromRule(priority int, from string, tableID int) netif.DesiredRule {
	return netif.DesiredRule{
		Family:   familyV6,
		Priority: priority,
		From:     from,
		Mark:     0,
		IifName:  "",
		UIDRange: "",
		Table:    "",
		TableID:  tableID,
	}
}

func fromRuleV4(priority int, from string, tableID int) netif.DesiredRule {
	return netif.DesiredRule{
		Family:   familyV4,
		Priority: priority,
		From:     from,
		Mark:     0,
		IifName:  "",
		UIDRange: "",
		Table:    "",
		TableID:  tableID,
	}
}

func catchAllRule(family string, iifName string, tableID int) netif.DesiredRule {
	return netif.DesiredRule{
		Family:   family,
		Priority: catchAllPriority,
		From:     "",
		Mark:     0,
		IifName:  iifName,
		UIDRange: "",
		Table:    "",
		TableID:  tableID,
	}
}

func configWithoutWebpassNPT(cfg Config) Config {
	cfg.WANs = append([]WAN(nil), cfg.WANs...)
	cfg.WANs[1].NptPrefix = ""
	return cfg
}

// configWithWebpassInTierOne moves Webpass down beside Monkeybrains, so the
// active tier can hold two healthy providers while not being the first
// configured tier.
func configWithWebpassInTierOne(cfg Config) Config {
	cfg.WANs = append([]WAN(nil), cfg.WANs...)
	cfg.WANs[1].Tier = 1
	return cfg
}

func testEnv() *ifmgr.Env {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &ifmgr.Env{
		Iface: "vmbr250",
		Log: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
		Alerts: ifmgr.WrapNotifier(notify.FromConfig(&config.Config{}, log, "mwan-ifmgr")),
	}
}

func testEnvWithStore(store *wanstate.Store) *ifmgr.Env {
	env := testEnv()
	env.LiveState = store
	return env
}
