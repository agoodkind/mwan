package moduleconfig

import (
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	health "goodkind.io/mwan/internal/ifmgr/modules/health"
	npt "goodkind.io/mwan/internal/ifmgr/modules/npt"
	steering "goodkind.io/mwan/internal/ifmgr/modules/steering"
	wanroutes "goodkind.io/mwan/internal/ifmgr/modules/wanroutes"
	"goodkind.io/mwan/internal/networkjson"
)

// sharedWANForTest supplies providers and shared network settings to builder tests.
func sharedWANForTest() config.IfMgrSection {
	return config.IfMgrSection{
		InternalPrefix: "3d06:bad:b01::/60",
		OpnsenseEdgeV6: "3d06:bad:b01:201::1",
		MwanbrEdgeV6:   "3d06:bad:b01:200::1",
		WAN: map[string]config.IfMgrWANEntry{
			"att": {
				Iface:         "att0",
				TableID:       100,
				FwMark:        1,
				FwMarkPrio:    100,
				FromPrio:      55,
				TranslationV6: testNPTPolicy("3d06:bad:b01:1100::/56"),
				V4Source:      "",
				Tier:          0,
				Weight:        1,
			},
			"webpass": {
				Iface:         "webpass0",
				TableID:       200,
				FwMark:        2,
				FwMarkPrio:    200,
				FromPrio:      56,
				TranslationV6: testNPTPolicy("3d06:bad:b01:2200::/56"),
				V4Source:      "203.0.113.2",
				Tier:          1,
				Weight:        3,
				TranslationV4: &config.IPv4Translation{
					Mode: config.TranslationNAPT44,
					StaticMappings: []config.StaticMapping{
						{
							External: netip.MustParseAddr("203.0.113.2"),
							Internal: netip.MustParseAddr("192.0.2.2"),
						},
						{
							External: netip.MustParseAddr("203.0.113.3"),
							Internal: netip.MustParseAddr("192.0.2.3"),
						},
					},
				},
			},
		},
	}
}

func ifmgrForTest(mods config.IfMgrModulesSection) config.IfMgrSection {
	s := sharedWANForTest()
	s.Modules = mods

	return s
}

func TestBuildWANRefs(t *testing.T) {
	t.Parallel()

	got := WANRefs(sharedWANForTest())
	want := WANInputs{
		InternalPrefix: "3d06:bad:b01::/60",
		OpnsenseEdgeV6: "3d06:bad:b01:201::1",
		MwanbrEdgeV6:   "3d06:bad:b01:200::1",
		WANs: []sharedWAN{
			{
				WANRef:        ifmgr.WANRef{ID: "att", Name: "att", Iface: "att0"},
				TableID:       100,
				FwMark:        1,
				FwMarkPrio:    100,
				FromPrio:      55,
				TranslationV6: testNPTPolicy("3d06:bad:b01:1100::/56"),
				Tier:          0,
				Weight:        1,
			},
			{
				WANRef:        ifmgr.WANRef{ID: "webpass", Name: "webpass", Iface: "webpass0"},
				TableID:       200,
				FwMark:        2,
				FwMarkPrio:    200,
				FromPrio:      56,
				TranslationV6: testNPTPolicy("3d06:bad:b01:2200::/56"),
				V4Source:      "203.0.113.2",
				Tier:          1,
				Weight:        3,
				TranslationV4: &config.IPv4Translation{
					Mode: config.TranslationNAPT44,
					StaticMappings: []config.StaticMapping{
						{
							External: netip.MustParseAddr("203.0.113.2"),
							Internal: netip.MustParseAddr("192.0.2.2"),
						},
						{
							External: netip.MustParseAddr("203.0.113.3"),
							Internal: netip.MustParseAddr("192.0.2.3"),
						},
					},
				},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("buildWANRefs mismatch\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestBuildWANRoutesConfig(t *testing.T) {
	t.Parallel()

	shared := WANRefs(sharedWANForTest())
	cfg, err := buildWANRoutesConfig(
		shared,
		&config.IfMgrWANRoutesSection{
			InternalIface:   "vmbr250",
			InternalNetV4:   "10.250.250.0/29",
			HealthStateFile: "/var/run/mwan-health.state",
		},
	)
	if err != nil {
		t.Fatalf("buildWANRoutesConfig returned error: %v", err)
	}

	// WAN route configuration reads provider routing data from the shared WAN map.
	want := wanroutes.Config{
		InternalIface:   "vmbr250",
		OpnsenseEdgeV6:  "3d06:bad:b01:201::1",
		InternalNetV4:   "10.250.250.0/29",
		HealthStateFile: "/var/run/mwan-health.state",
		WANs: []wanroutes.WAN{
			{
				WANRef:        ifmgr.WANRef{ID: "att", Name: "att", Iface: "att0"},
				TableID:       100,
				FwMark:        1,
				FwMarkPrio:    100,
				FromPrio:      55,
				TranslationV6: testNPTPolicy("3d06:bad:b01:1100::/56"),
				Tier:          0,
				Weight:        1,
			},
			{
				WANRef:        ifmgr.WANRef{ID: "webpass", Name: "webpass", Iface: "webpass0"},
				TableID:       200,
				FwMark:        2,
				FwMarkPrio:    200,
				FromPrio:      56,
				TranslationV6: testNPTPolicy("3d06:bad:b01:2200::/56"),
				V4Source:      "203.0.113.2",
				Tier:          1,
				Weight:        3,
				TranslationV4: &config.IPv4Translation{
					Mode: config.TranslationNAPT44,
					StaticMappings: []config.StaticMapping{
						{
							External: netip.MustParseAddr("203.0.113.2"),
							Internal: netip.MustParseAddr("192.0.2.2"),
						},
						{
							External: netip.MustParseAddr("203.0.113.3"),
							Internal: netip.MustParseAddr("192.0.2.3"),
						},
					},
				},
				MappedExternals: []netip.Addr{
					netip.MustParseAddr("203.0.113.2"),
					netip.MustParseAddr("203.0.113.3"),
				},
			},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("buildWANRoutesConfig mismatch\ngot:  %#v\nwant: %#v", cfg, want)
	}
}

func TestBuildWANRoutesConfigNilSection(t *testing.T) {
	t.Parallel()

	cfg, err := buildWANRoutesConfig(WANRefs(sharedWANForTest()), nil)
	if err != nil {
		t.Fatalf("buildWANRoutesConfig returned error: %v", err)
	}

	want := wanroutes.Config{}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("buildWANRoutesConfig nil = %#v, want %#v", cfg, want)
	}
}

func TestBuildIfMgrModuleConfigsUnknownRole(t *testing.T) {
	t.Parallel()

	if _, err := Build(config.IfMgrSection{}, "bogus"); err == nil {
		t.Fatal("Build with an unknown role must error")
	}
}

func TestBuildHealthConfig(t *testing.T) {
	t.Parallel()

	shared := WANRefs(sharedWANForTest())
	cfg, err := HealthConfig(
		shared,
		&config.IfMgrHealthSection{
			StateFile:          "/run/health",
			ProbeTimeoutMillis: 3000,
			WAN: map[string]config.IfMgrHealthWANSection{
				"att": {
					Enabled:              true,
					PingCount:            new(4),
					SuccessThreshold:     new(2),
					CheckIntervalSeconds: new(15),
					FailureThreshold:     new(3),
					RecoveryThreshold:    new(4),
					TargetsV4:            []string{"192.0.2.1", "192.0.2.2"},
					TargetsV6:            []string{"2001:db8::1", "2001:db8::2"},
					HTTPURLs:             []string{"https://example.com/health"},
				},
				"webpass": {
					Enabled:              true,
					PingCount:            new(5),
					SuccessThreshold:     new(1),
					CheckIntervalSeconds: new(30),
					FailureThreshold:     new(5),
					RecoveryThreshold:    new(3),
					TargetsV4:            []string{"198.51.100.1", "198.51.100.2"},
					TargetsV6:            []string{"2001:db8:1::1", "2001:db8:1::2"},
					HTTPURLs:             []string{"https://example.net/health"},
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("buildHealthConfig returned error: %v", err)
	}

	want := health.Config{
		StateFile:         "/run/health",
		TargetsV4:         nil,
		TargetsV6:         nil,
		HTTPURLs:          nil,
		Timeout:           3 * time.Second,
		Interval:          15 * time.Second,
		PingCount:         0,
		SuccessThreshold:  0,
		FailureThreshold:  0,
		RecoveryThreshold: 0,
		WANs: []health.WAN{
			{
				WANRef: ifmgr.WANRef{ID: "att", Name: "att", Iface: "att0"},
				Tier:   0,
				TargetsV4: []netip.Addr{
					netip.MustParseAddr("192.0.2.1"),
					netip.MustParseAddr("192.0.2.2"),
				},
				TargetsV6: []netip.Addr{
					netip.MustParseAddr("2001:db8::1"),
					netip.MustParseAddr("2001:db8::2"),
				},
				HTTPURLs:          []string{"https://example.com/health"},
				PingCount:         4,
				SuccessThreshold:  2,
				FailureThreshold:  3,
				RecoveryThreshold: 4,
				CheckInterval:     15 * time.Second,
			},
			{
				WANRef: ifmgr.WANRef{ID: "webpass", Name: "webpass", Iface: "webpass0"},
				Tier:   1,
				TargetsV4: []netip.Addr{
					netip.MustParseAddr("198.51.100.1"),
					netip.MustParseAddr("198.51.100.2"),
				},
				TargetsV6: []netip.Addr{
					netip.MustParseAddr("2001:db8:1::1"),
					netip.MustParseAddr("2001:db8:1::2"),
				},
				HTTPURLs:          []string{"https://example.net/health"},
				PingCount:         5,
				SuccessThreshold:  1,
				FailureThreshold:  5,
				RecoveryThreshold: 3,
				CheckInterval:     30 * time.Second,
			},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("buildHealthConfig mismatch\ngot:  %#v\nwant: %#v", cfg, want)
	}
}

// enabledHealthWANSection returns settings that pass HealthConfig validation.
func enabledHealthWANSection(intervalSeconds int) config.IfMgrHealthWANSection {
	return config.IfMgrHealthWANSection{
		Enabled:              true,
		PingCount:            new(3),
		SuccessThreshold:     new(2),
		CheckIntervalSeconds: new(intervalSeconds),
		FailureThreshold:     new(2),
		RecoveryThreshold:    new(2),
		TargetsV4:            []string{"1.1.1.1", "8.8.8.8"},
		TargetsV6:            []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
		HTTPURLs:             []string{"https://ifconfig.co/ip"},
	}
}

func TestBuildHealthConfigSkipsDisabledAndAbsentWANs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		wan      map[string]config.IfMgrHealthWANSection
		wantName string
	}{
		{
			name: "disabled WAN",
			wan: map[string]config.IfMgrHealthWANSection{
				"att":     {Enabled: false, CheckIntervalSeconds: new(10)},
				"webpass": enabledHealthWANSection(30),
			},
			wantName: "webpass",
		},
		{
			name: "absent WAN",
			wan: map[string]config.IfMgrHealthWANSection{
				"att": enabledHealthWANSection(10),
			},
			wantName: "att",
		},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				t.Parallel()

				cfg, err := HealthConfig(
					WANRefs(sharedWANForTest()),
					&config.IfMgrHealthSection{WAN: test.wan},
				)
				if err != nil {
					t.Fatalf("buildHealthConfig returned error: %v", err)
				}

				if len(cfg.WANs) != 1 {
					t.Fatalf("WAN count = %d, want 1", len(cfg.WANs))
				}

				if cfg.WANs[0].Name != test.wantName {
					t.Fatalf("included WAN = %q, want %q", cfg.WANs[0].Name, test.wantName)
				}
			},
		)
	}
}

func TestBuildHealthConfigRejectsUnderspecifiedEnabledWAN(t *testing.T) {
	t.Parallel()

	base := enabledHealthWANSection(10)

	tests := []struct {
		name    string
		mutate  func(s config.IfMgrHealthWANSection) config.IfMgrHealthWANSection
		wantSub string
	}{
		{
			name: "zero ping_count",
			mutate: func(s config.IfMgrHealthWANSection) config.IfMgrHealthWANSection {
				s.PingCount = new(0)

				return s
			},
			wantSub: "health/ping-count",
		},
		{
			name: "zero success_threshold",
			mutate: func(s config.IfMgrHealthWANSection) config.IfMgrHealthWANSection {
				s.SuccessThreshold = new(0)

				return s
			},
			wantSub: "health/success-threshold",
		},
		{
			name: "no targets in either family",
			mutate: func(s config.IfMgrHealthWANSection) config.IfMgrHealthWANSection {
				s.TargetsV4 = nil
				s.TargetsV6 = nil

				return s
			},
			wantSub: "must have at least one targets-v4 or targets-v6 entry",
		},
		{
			name: "success_threshold exceeds targets",
			mutate: func(s config.IfMgrHealthWANSection) config.IfMgrHealthWANSection {
				s.SuccessThreshold = new(3)

				return s
			},
			wantSub: "exceeds targets-v4",
		},
	}
	for _, test := range tests {
		t.Run(
			test.name,
			func(t *testing.T) {
				t.Parallel()

				_, err := HealthConfig(
					WANRefs(sharedWANForTest()),
					&config.IfMgrHealthSection{
						WAN: map[string]config.IfMgrHealthWANSection{
							"att": test.mutate(base),
						},
					},
				)
				if err == nil {
					t.Fatal("buildHealthConfig must reject an under-specified enabled WAN")
				}

				if !strings.Contains(err.Error(), test.wantSub) {
					t.Fatalf("error %q does not mention %q", err, test.wantSub)
				}
			},
		)
	}
}

func TestBuildHealthConfigAcceptsSingleFamilyProvider(t *testing.T) {
	t.Parallel()

	section := enabledHealthWANSection(10)
	section.TargetsV6 = []string{}
	cfg, err := HealthConfig(
		WANRefs(sharedWANForTest()),
		&config.IfMgrHealthSection{
			WAN: map[string]config.IfMgrHealthWANSection{"att": section},
		},
	)
	if err != nil {
		t.Fatalf("HealthConfig rejected an IPv4-only provider: %v", err)
	}

	if len(cfg.WANs) != 1 {
		t.Fatalf("The configuration contains %d WAN entries. The test expects 1.", len(cfg.WANs))
	}

	if cfg.WANs[0].TargetsV6 == nil {
		t.Fatal("The provider configured an empty IPv6 target list, but the module received nil.")
	}

	if len(cfg.WANs[0].TargetsV6) != 0 {
		t.Fatalf("targets-v6 = %v, want empty", cfg.WANs[0].TargetsV6)
	}

	if len(cfg.WANs[0].TargetsV4) != 2 {
		t.Fatalf("targets-v4 count = %d, want 2", len(cfg.WANs[0].TargetsV4))
	}
}

// The health module uses its module-wide IPv6 targets when the provider list is nil.
func TestBuildHealthConfigKeepsAnOmittedTargetFamilyNil(t *testing.T) {
	t.Parallel()

	section := enabledHealthWANSection(10)
	section.TargetsV6 = nil
	cfg, err := HealthConfig(
		WANRefs(sharedWANForTest()),
		&config.IfMgrHealthSection{
			WAN: map[string]config.IfMgrHealthWANSection{"att": section},
		},
	)
	if err != nil {
		t.Fatalf("buildHealthConfig returned error: %v", err)
	}

	if len(cfg.WANs) != 1 {
		t.Fatalf("WAN count = %d, want 1", len(cfg.WANs))
	}

	if cfg.WANs[0].TargetsV6 != nil {
		t.Fatalf(
			"The provider IPv6 target list must remain nil when omitted, but it is %v. A nil list tells the health module to use the module-wide IPv6 target list.",
			cfg.WANs[0].TargetsV6,
		)
	}
}

func TestBuildHealthConfigNilSection(t *testing.T) {
	t.Parallel()

	cfg, err := HealthConfig(WANRefs(sharedWANForTest()), nil)
	if err != nil {
		t.Fatalf("buildHealthConfig returned error: %v", err)
	}

	if len(cfg.WANs) != 2 {
		t.Fatalf("WAN count = %d, want 2 from the shared list", len(cfg.WANs))
	}
}

func TestBuildIfMgrModuleConfigsWANRoleBuildsAll(t *testing.T) {
	t.Parallel()

	modules := config.IfMgrModulesSection{
		Health: &config.IfMgrHealthSection{},
		WAN: &config.IfMgrModulesWANSection{
			Routes: &config.IfMgrWANRoutesSection{InternalIface: "enmwanbr0"},
		},
	}
	set, err := Build(ifmgrForTest(modules), "wan")
	if err != nil {
		t.Fatalf("Build(wan) returned error: %v", err)
	}

	if _, ok := set["wan.routes"]; !ok {
		t.Fatal("wan role must build a wan.routes config")
	}

	healthCfg, ok := set["health"]
	if !ok {
		t.Fatal("wan role must build a health config")
	}

	if _, ok := healthCfg.(health.Config); !ok {
		t.Fatalf("health config type = %T, want health.Config", healthCfg)
	}

	nptCfg, ok := set["npt"]
	if !ok {
		t.Fatal("wan role must build an npt config")
	}

	if _, ok := nptCfg.(npt.Config); !ok {
		t.Fatalf("npt config type = %T, want npt.Config", nptCfg)
	}
}

func TestBuildSteeringConfig(t *testing.T) {
	t.Parallel()

	ifmgrCfg := sharedWANForTest()
	ifmgrCfg.HashMode = "source"
	shared := WANRefs(ifmgrCfg)
	cfg, err := buildSteeringConfig(
		shared,
		ifmgrCfg,
		&config.IfMgrWANRoutesSection{
			InternalIface:   "vmbr250",
			InternalNetV4:   "10.250.250.0/29",
			HealthStateFile: "/var/run/mwan-health.state",
		},
	)
	if err != nil {
		t.Fatalf("buildSteeringConfig returned error: %v", err)
	}

	want := steering.Config{
		InternalIface:   "vmbr250",
		InternalNetV4:   "10.250.250.0/29",
		InternalPrefix:  "3d06:bad:b01::/60",
		OpnsenseEdgeV6:  "3d06:bad:b01:201::1",
		HashMode:        "source",
		HealthStateFile: "/var/run/mwan-health.state",
		Members: []steering.Member{
			{
				WANRef: ifmgr.WANRef{ID: "att", Name: "att", Iface: "att0"},
				Mark:   1,
				Tier:   0,
				Weight: 1,
			},
			{
				WANRef: ifmgr.WANRef{ID: "webpass", Name: "webpass", Iface: "webpass0"},
				Mark:   2,
				Tier:   1,
				Weight: 3,
			},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("buildSteeringConfig mismatch\ngot:  %#v\nwant: %#v", cfg, want)
	}
}

func TestBuildIfMgrModuleConfigsWANRoleBuildsSteering(t *testing.T) {
	t.Parallel()

	ifmgrCfg := ifmgrForTest(config.IfMgrModulesSection{
		WAN: &config.IfMgrModulesWANSection{
			Routes: &config.IfMgrWANRoutesSection{InternalIface: "enmwanbr0"},
		},
	})
	ifmgrCfg.HashMode = "random"
	set, err := Build(ifmgrCfg, "wan")
	if err != nil {
		t.Fatalf("Build(wan) returned error: %v", err)
	}

	steeringCfg, isSteering := set["steering"].(steering.Config)
	if !isSteering {
		t.Fatalf("wan role built %T for steering, want steering.Config", set["steering"])
	}

	if steeringCfg.HashMode != "random" {
		t.Fatalf("steering hash mode = %q, want random", steeringCfg.HashMode)
	}

	if len(steeringCfg.Members) != 2 {
		t.Fatalf("steering member count = %d, want 2", len(steeringCfg.Members))
	}
}

func TestNetworkConfigOwnsTheNetworkTree(t *testing.T) {
	t.Parallel()

	const configTOML = `
[ifmgr]
role = "wan"
internal_prefix = "3d06:bad:b01:999::/60"
opnsense_edge_v6 = "3d06:bad:b01:999::2"
mwanbr_edge_v6 = "3d06:bad:b01:999::3"

[ifmgr.iface.enmbrains0]
name = "enmbrains0"

[ifmgr.wan.att]
iface = "stale0"
table_id = 999

[ifmgr.modules.wan.routes]
internal_iface = "stale0"
internal_net_v4 = "192.0.2.240/29"
health_state_file = "/run/mwan-health.state"

[ifmgr.modules.health]
state_file = "/run/mwan-health.state"
persist_state_file = "/var/lib/mwan/health-state"
timeout = "2s"

[ifmgr.modules.health.wan.att]
enabled = true
ping_count = 99
`
	var cfg config.Config
	if err := toml.Unmarshal([]byte(configTOML), &cfg); err != nil {
		t.Fatalf("toml.Unmarshal: %v", err)
	}

	if len(cfg.IfMgr.WAN) != 0 {
		t.Fatalf("[ifmgr.wan] still decodes from TOML: %#v", cfg.IfMgr.WAN)
	}

	if cfg.IfMgr.InternalPrefix != "" {
		t.Fatalf("internal_prefix still decodes from TOML: %q", cfg.IfMgr.InternalPrefix)
	}

	if cfg.IfMgr.Modules.WAN.Routes.InternalIface != "" {
		t.Fatalf(
			"internal_iface still decodes from TOML: %q",
			cfg.IfMgr.Modules.WAN.Routes.InternalIface,
		)
	}

	if len(cfg.IfMgr.Modules.Health.WAN) != 0 {
		t.Fatalf(
			"[ifmgr.modules.health.wan] still decodes from TOML: %#v",
			cfg.IfMgr.Modules.Health.WAN,
		)
	}

	// TOML retains the health and route state paths.
	if cfg.IfMgr.Modules.Health.StateFile != "/run/mwan-health.state" {
		t.Fatalf("health state_file did not parse: %q", cfg.IfMgr.Modules.Health.StateFile)
	}

	if cfg.IfMgr.Modules.WAN.Routes.HealthStateFile != "/run/mwan-health.state" {
		t.Fatalf(
			"wan.routes health_state_file did not parse: %q",
			cfg.IfMgr.Modules.WAN.Routes.HealthStateFile,
		)
	}

	network := networkjson.Config{
		InternalPrefix:     "3d06:bad:b01:210::/60",
		OpnsenseEdgeV6:     "3d06:bad:b01:201::2",
		MwanbrEdgeV6:       "3d06:bad:b01:201::3",
		InternalIface:      "enmwanbr0",
		InternalNetV4:      "192.0.2.0/29",
		ProbeTimeoutMillis: 2000,
		WAN: map[string]config.IfMgrWANEntry{
			"att": {
				Iface:         "enatt0",
				TableID:       100,
				FwMark:        1,
				FwMarkPrio:    100,
				FromPrio:      55,
				TranslationV6: testNPTPolicy("3d06:bad:b01:2300::/60"),
				V4Source:      "",
			},
			"webpass": {
				Iface:         "enwebpass0",
				TableID:       200,
				FwMark:        2,
				FwMarkPrio:    200,
				FromPrio:      56,
				TranslationV6: testNPTPolicy("3d06:bad:b01:2200::/60"),
				V4Source:      "10.241.204.2",
			},
		},
		Health: map[string]config.IfMgrHealthWANSection{
			"att": {
				Enabled:              true,
				PingCount:            new(3),
				SuccessThreshold:     new(2),
				CheckIntervalSeconds: new(10),
				FailureThreshold:     new(2),
				RecoveryThreshold:    new(2),
				TargetsV4:            []string{"1.1.1.1", "8.8.8.8"},
				TargetsV6:            []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
				HTTPURLs:             []string{"https://ifconfig.co/ip"},
			},
			"webpass": {
				Enabled:              true,
				PingCount:            new(3),
				SuccessThreshold:     new(2),
				CheckIntervalSeconds: new(30),
				FailureThreshold:     new(2),
				RecoveryThreshold:    new(2),
				TargetsV4:            []string{"1.1.1.1", "8.8.8.8"},
				TargetsV6:            []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
				HTTPURLs:             []string{"https://ifconfig.co/ip"},
			},
		},
	}
	network.Apply(&cfg)

	set, err := Build(cfg.IfMgr, "wan")
	if err != nil {
		t.Fatalf("Build(wan): %v", err)
	}

	wr, ok := set["wan.routes"].(wanroutes.Config)
	if !ok {
		t.Fatalf("wan.routes config missing or wrong type: %T", set["wan.routes"])
	}

	if wr.InternalIface != "enmwanbr0" {
		t.Fatalf("wan.routes internal iface = %q, want enmwanbr0", wr.InternalIface)
	}

	if wr.HealthStateFile != "/run/mwan-health.state" {
		t.Fatalf(
			"wan.routes health state file = %q, want /run/mwan-health.state",
			wr.HealthStateFile,
		)
	}

	byName := map[string]wanroutes.WAN{}

	for _, w := range wr.WANs {
		byName[w.Name] = w
	}

	if byName["att"].Iface != "enatt0" || byName["webpass"].Iface != "enwebpass0" {
		t.Fatalf("wan.routes ifaces did not resolve from the network file: %#v", byName)
	}

	if byName["att"].TableID != 100 || byName["webpass"].V4Source != "10.241.204.2" {
		t.Fatalf("wan.routes routing fields did not resolve from the network file: %#v", byName)
	}

	hc, ok := set["health"].(health.Config)
	if !ok {
		t.Fatalf("health config missing or wrong type: %T", set["health"])
	}

	if hc.Timeout != 2*time.Second {
		t.Fatalf("health timeout = %s, want 2s", hc.Timeout)
	}

	if hc.Interval != 10*time.Second {
		t.Fatalf("health interval = %s, want the shortest provider interval 10s", hc.Interval)
	}

	if _, ok := set["npt"]; !ok {
		t.Fatal("wan role must build an npt config from the two-file load")
	}
}

// TestGatewayLoadWithoutNetworkTOML checks module configuration from network data
// while TOML supplies the health and route state paths.
func TestGatewayLoadWithoutNetworkTOML(t *testing.T) {
	t.Parallel()

	const configTOML = `
[ifmgr]
role = "wan"

[ifmgr.iface.enmbrains0]
name = "enmbrains0"

[ifmgr.modules.wan.routes]
health_state_file = "/run/mwan-health.state"

[ifmgr.modules.health]
state_file = "/run/mwan-health.state"
`
	var cfg config.Config
	if err := toml.Unmarshal([]byte(configTOML), &cfg); err != nil {
		t.Fatalf("toml.Unmarshal: %v", err)
	}

	network := networkjson.Config{
		InternalPrefix:     "3d06:bad:b01:210::/60",
		OpnsenseEdgeV6:     "3d06:bad:b01:201::2",
		MwanbrEdgeV6:       "3d06:bad:b01:201::3",
		InternalIface:      "enmwanbr0",
		InternalNetV4:      "192.0.2.0/29",
		ProbeTimeoutMillis: 2000,
		WAN: map[string]config.IfMgrWANEntry{
			"att": {
				Iface:         "enatt0",
				TableID:       100,
				FwMark:        1,
				FwMarkPrio:    100,
				FromPrio:      55,
				TranslationV6: testNPTPolicy("3d06:bad:b01:2300::/60"),
				V4Source:      "",
			},
		},
		Health: map[string]config.IfMgrHealthWANSection{
			"att": {
				Enabled:              true,
				PingCount:            new(3),
				SuccessThreshold:     new(2),
				CheckIntervalSeconds: new(10),
				FailureThreshold:     new(2),
				RecoveryThreshold:    new(2),
				TargetsV4:            []string{"1.1.1.1", "8.8.8.8"},
				TargetsV6:            []string{"2606:4700:4700::1111", "2001:4860:4860::8888"},
				HTTPURLs:             []string{"https://ifconfig.co/ip"},
			},
		},
	}
	network.Apply(&cfg)

	set, err := Build(cfg.IfMgr, "wan")
	if err != nil {
		t.Fatalf("Build(wan): %v", err)
	}

	nptConfig, ok := set["npt"].(npt.Config)
	if !ok {
		t.Fatalf("npt config missing or wrong type: %T", set["npt"])
	}

	if nptConfig.InternalPrefix != "3d06:bad:b01:210::/60" {
		t.Fatalf(
			"npt internal prefix = %q, want the network file's value",
			nptConfig.InternalPrefix,
		)
	}

	if len(nptConfig.WANs) != 1 || nptConfig.WANs[0].Iface != "enatt0" {
		t.Fatalf("npt WAN list did not resolve: %#v", nptConfig.WANs)
	}

	wr, ok := set["wan.routes"].(wanroutes.Config)
	if !ok {
		t.Fatalf("wan.routes config missing or wrong type: %T", set["wan.routes"])
	}

	if wr.InternalNetV4 != "192.0.2.0/29" {
		t.Fatalf("wan.routes internal net = %q, want the network file's value", wr.InternalNetV4)
	}

	if wr.HealthStateFile != "/run/mwan-health.state" {
		t.Fatalf("wan.routes health state file = %q, want TOML's value", wr.HealthStateFile)
	}

	hc, ok := set["health"].(health.Config)
	if !ok {
		t.Fatalf("health config missing or wrong type: %T", set["health"])
	}

	if hc.StateFile != "/run/mwan-health.state" {
		t.Fatalf("health state file = %q, want TOML's value", hc.StateFile)
	}

	if len(hc.WANs) != 1 || hc.WANs[0].CheckInterval != 10*time.Second {
		t.Fatalf("health WAN list did not resolve: %#v", hc.WANs)
	}
}

func testNPTPolicy(external string) *config.IPv6Translation {
	return &config.IPv6Translation{
		Mode: config.TranslationNPTv6,
		NPT: &config.NPTv6Translation{
			InternalPrefix: netip.MustParsePrefix("3d06:bad:b01::/60"),
			ExternalSource: config.PrefixDelegated,
			ExpectedPrefix: netip.MustParsePrefix(external),
		},
	}
}
