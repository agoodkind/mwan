package main

import (
	"errors"
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

func TestBuildPolicyRuleUIDRangeUsesStaticRange(t *testing.T) {
	t.Parallel()

	rule := config.IfMgrPolicyRuleSection{UIDRange: "997-997"}
	got, err := buildPolicyRuleUIDRange(rule, func(string) (string, error) {
		return "", errors.New("lookup should not run")
	})
	if err != nil {
		t.Fatalf("buildPolicyRuleUIDRange returned error: %v", err)
	}
	if got != "997-997" {
		t.Fatalf("buildPolicyRuleUIDRange returned %q, want %q", got, "997-997")
	}
}

func TestBuildPolicyRuleUIDRangeUsesUser(t *testing.T) {
	t.Parallel()

	rule := config.IfMgrPolicyRuleSection{UIDUser: "cloudflared-oob"}
	got, err := buildPolicyRuleUIDRange(rule, func(username string) (string, error) {
		if username != "cloudflared-oob" {
			t.Fatalf("lookup username = %q, want %q", username, "cloudflared-oob")
		}
		return "997", nil
	})
	if err != nil {
		t.Fatalf("buildPolicyRuleUIDRange returned error: %v", err)
	}
	if got != "997-997" {
		t.Fatalf("buildPolicyRuleUIDRange returned %q, want %q", got, "997-997")
	}
}

func TestBuildPolicyRuleUIDRangeRejectsConflictingSelectors(t *testing.T) {
	t.Parallel()

	rule := config.IfMgrPolicyRuleSection{
		UIDRange: "997-997",
		UIDUser:  "cloudflared-oob",
	}
	_, err := buildPolicyRuleUIDRange(rule, func(string) (string, error) {
		return "997", nil
	})
	if err == nil {
		t.Fatal("buildPolicyRuleUIDRange returned nil error")
	}
}

func TestBuildPolicyRuleUIDRangeRejectsInvalidUID(t *testing.T) {
	t.Parallel()

	rule := config.IfMgrPolicyRuleSection{UIDUser: "cloudflared-oob"}
	_, err := buildPolicyRuleUIDRange(rule, func(string) (string, error) {
		return "not-a-number", nil
	})
	if err == nil {
		t.Fatal("buildPolicyRuleUIDRange returned nil error")
	}
}

func TestBuildHostIPv6PolicyConfig(t *testing.T) {
	t.Parallel()

	cfg, err := buildHostIPv6PolicyConfig(&config.IfMgrHostIPv6PolicySection{
		MissingIfaceGracePeriod: "3m",
		Interface: []config.IfMgrHostIPv6PolicyIfaceSection{
			{
				Name:             "vmbr0",
				AcceptRA:         2,
				AutoConf:         true,
				AcceptRADefRtr:   true,
				SolicitRA:        true,
				CleanupRADefault: false,
			},
			{
				Name:             "vmbr4",
				AcceptRA:         0,
				AutoConf:         false,
				AcceptRADefRtr:   false,
				SolicitRA:        false,
				CleanupRADefault: true,
			},
		},
	})
	if err != nil {
		t.Fatalf("buildHostIPv6PolicyConfig returned error: %v", err)
	}
	if got := cfg.MissingIfaceGracePeriod; got != 3*time.Minute {
		t.Fatalf("MissingIfaceGracePeriod = %s, want %s", got, 3*time.Minute)
	}
	if len(cfg.Policies) != 2 {
		t.Fatalf("policy count = %d, want 2", len(cfg.Policies))
	}
	if got := cfg.Policies[0].Name; got != "vmbr0" {
		t.Fatalf("first policy iface = %q, want %q", got, "vmbr0")
	}
	if got := cfg.Policies[1].CleanupRADefault; !got {
		t.Fatal("second policy should clean denied RA defaults")
	}
}

// sharedWANForTest is the [ifmgr] shared per-WAN foundation both module builders
// read: the WAN map ([ifmgr.wan.<name>]) with each WAN's full config (iface plus
// the routing slots wan.routes owns), plus the shared edge addresses and internal
// prefix on [ifmgr] itself. One home per WAN; modules read the fields they need.
func sharedWANForTest() config.IfMgrSection {
	return config.IfMgrSection{
		InternalPrefix: "3d06:bad:b01::/60",
		OpnsenseEdgeV6: "3d06:bad:b01:201::1",
		MwanbrEdgeV6:   "3d06:bad:b01:200::1",
		WAN: map[string]config.IfMgrWANEntry{
			"att": {
				Iface:      "att0",
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:1100::/56",
				V4Source:   "",
				Tier:       0,
				Weight:     1,
			},
			"webpass": {
				Iface:      "webpass0",
				TableID:    200,
				FwMark:     2,
				FwMarkPrio: 200,
				FromPrio:   56,
				NptPrefix:  "3d06:bad:b01:2200::/56",
				V4Source:   "203.0.113.2",
				Tier:       1,
				Weight:     3,
				StaticMappings: []config.StaticMapping{
					{External: netip.MustParseAddr("203.0.113.2"), Internal: netip.MustParseAddr("192.0.2.2")},
					{External: netip.MustParseAddr("203.0.113.3"), Internal: netip.MustParseAddr("192.0.2.3")},
				},
			},
		},
	}
}

// ifmgrForTest is sharedWANForTest with the given modules attached, for the
// role-scoped buildIfMgrModuleConfigs tests.
func ifmgrForTest(mods config.IfMgrModulesSection) config.IfMgrSection {
	s := sharedWANForTest()
	s.Modules = mods
	return s
}

// TestBuildWANRefs pins that the generic per-WAN builder turns the shared
// [ifmgr.wan] map into the sorted per-WAN list (identity plus routing fields)
// and the shared prefixes every module builder reuses.
func TestBuildWANRefs(t *testing.T) {
	t.Parallel()

	got := buildWANRefs(sharedWANForTest())
	want := sharedWANInputs{
		InternalPrefix: "3d06:bad:b01::/60",
		OpnsenseEdgeV6: "3d06:bad:b01:201::1",
		MwanbrEdgeV6:   "3d06:bad:b01:200::1",
		WANs: []sharedWAN{
			{
				WANRef:     ifmgr.WANRef{Name: "att", Iface: "att0"},
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:1100::/56",
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
				Tier:       1,
				Weight:     3,
				StaticMappings: []config.StaticMapping{
					{External: netip.MustParseAddr("203.0.113.2"), Internal: netip.MustParseAddr("192.0.2.2")},
					{External: netip.MustParseAddr("203.0.113.3"), Internal: netip.MustParseAddr("192.0.2.3")},
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

	shared := buildWANRefs(sharedWANForTest())
	cfg, err := buildWANRoutesConfig(shared, &config.IfMgrWANRoutesSection{
		InternalIface:   "vmbr250",
		InternalNetV4:   "10.250.250.0/29",
		HealthStateFile: "/var/run/mwan-health.state",
	})
	if err != nil {
		t.Fatalf("buildWANRoutesConfig returned error: %v", err)
	}

	// The per-WAN routing data comes from the shared [ifmgr.wan.<name>] map
	// (sharedWANForTest), not a wan.routes-local list.
	want := wanroutes.Config{
		InternalIface:   "vmbr250",
		OpnsenseEdgeV6:  "3d06:bad:b01:201::1",
		InternalNetV4:   "10.250.250.0/29",
		HealthStateFile: "/var/run/mwan-health.state",
		WANs: []wanroutes.WAN{
			{
				WANRef:     ifmgr.WANRef{Name: "att", Iface: "att0"},
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:1100::/56",
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
				Tier:       1,
				Weight:     3,
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

	cfg, err := buildWANRoutesConfig(buildWANRefs(sharedWANForTest()), nil)
	if err != nil {
		t.Fatalf("buildWANRoutesConfig returned error: %v", err)
	}
	want := wanroutes.Config{}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("buildWANRoutesConfig nil = %#v, want %#v", cfg, want)
	}
}

// modulesWithUnresolvableUIDRule is a [ifmgr.modules] section that carries a
// policy_rules rule referencing a user that does not exist on the build host,
// plus a wan.routes section. It models the production MWAN VM config, where the
// shared config.toml carries an oob policy_rules rule (cloudflared-oob, a
// hypervisor-host user) even though the VM only runs the wan role.
func modulesWithUnresolvableUIDRule() config.IfMgrModulesSection {
	return config.IfMgrModulesSection{
		PolicyRules: &config.IfMgrPolicyRulesSection{
			Rule: []config.IfMgrPolicyRuleSection{
				{
					Family:   "inet6",
					Priority: 5,
					UIDUser:  "mwan-test-no-such-user",
					Table:    "oob",
					TableID:  500,
				},
			},
		},
		WAN: &config.IfMgrModulesWANSection{Routes: &config.IfMgrWANRoutesSection{InternalIface: "enmwanbr0"}},
	}
}

// TestBuildIfMgrModuleConfigsWANRoleSkipsPolicyRules is the regression test for
// the mwan-ifmgr@wan crash-loop. The wan role must build only wan.routes, so it
// never resolves the policy_rules uid_user (which would fail on a host lacking
// that user) even when the shared config carries that rule.
func TestBuildIfMgrModuleConfigsWANRoleSkipsPolicyRules(t *testing.T) {
	t.Parallel()

	set, err := buildIfMgrModuleConfigs(ifmgrForTest(modulesWithUnresolvableUIDRule()), "wan")
	if err != nil {
		t.Fatalf("buildIfMgrModuleConfigs(wan) returned error: %v", err)
	}
	if _, ok := set["policy_rules"]; ok {
		t.Fatal("wan role must not build a policy_rules config")
	}
	if _, ok := set["wan.routes"]; !ok {
		t.Fatal("wan role must build a wan.routes config")
	}
}

// TestBuildIfMgrModuleConfigsOOBRoleBuildsPolicyRules pins that the oob role
// does build policy_rules (and surfaces the uid lookup failure), so the
// role-scoped build does not silently drop a module the role actually runs.
func TestBuildIfMgrModuleConfigsOOBRoleBuildsPolicyRules(t *testing.T) {
	t.Parallel()

	_, err := buildIfMgrModuleConfigs(ifmgrForTest(modulesWithUnresolvableUIDRule()), "oob")
	if err == nil {
		t.Fatal("oob role must build policy_rules and surface the uid lookup failure")
	}
	if !strings.Contains(err.Error(), "policy_rules") {
		t.Fatalf("oob build error = %q, want it to mention policy_rules", err)
	}
}

// TestBuildIfMgrModuleConfigsUnknownRole confirms an unknown role is rejected
// rather than silently producing an empty config set.
func TestBuildIfMgrModuleConfigsUnknownRole(t *testing.T) {
	t.Parallel()

	if _, err := buildIfMgrModuleConfigs(config.IfMgrSection{}, "bogus"); err == nil {
		t.Fatal("buildIfMgrModuleConfigs with an unknown role must error")
	}
}

// TestBuildNPTConfig pins that the npt builder projects the shared [ifmgr.wan]
// prefixes, the WAN identity list, and each provider's configured translation
// prefix, which is what tells npt whether a missing delegation is a fault. This
// is also what makes MwanbrEdgeV6 a real consumer of the shared field.
func TestBuildNPTConfig(t *testing.T) {
	t.Parallel()

	shared := buildWANRefs(sharedWANForTest())
	cfg := buildNPTConfig(shared)

	want := npt.Config{
		InternalPrefix: "3d06:bad:b01::/60",
		OpnsenseEdgeV6: "3d06:bad:b01:201::1",
		MwanbrEdgeV6:   "3d06:bad:b01:200::1",
		WANs: []npt.WAN{
			{
				WANRef:    ifmgr.WANRef{Name: "att", Iface: "att0"},
				NptPrefix: "3d06:bad:b01:1100::/56",
			},
			{
				WANRef:    ifmgr.WANRef{Name: "webpass", Iface: "webpass0"},
				NptPrefix: "3d06:bad:b01:2200::/56",
			},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("buildNPTConfig mismatch\ngot:  %#v\nwant: %#v", cfg, want)
	}
}

// TestBuildNPTConfigCarriesAnEmptyPrefixForAnUntranslatedProvider pins that a
// provider the configuration assigns no npt-prefix reaches npt with an empty
// prefix, which is what keeps npt from alerting on a delegation it never
// expects.
func TestBuildNPTConfigCarriesAnEmptyPrefixForAnUntranslatedProvider(t *testing.T) {
	t.Parallel()

	section := sharedWANForTest()
	untranslated := section.WAN["webpass"]
	untranslated.NptPrefix = ""
	section.WAN["webpass"] = untranslated

	cfg := buildNPTConfig(buildWANRefs(section))
	if len(cfg.WANs) != 2 {
		t.Fatalf("npt WAN count = %d, want 2", len(cfg.WANs))
	}
	if got := cfg.WANs[0].NptPrefix; got != "3d06:bad:b01:1100::/56" {
		t.Fatalf("att npt prefix = %q, want the configured prefix", got)
	}
	if got := cfg.WANs[1].NptPrefix; got != "" {
		t.Fatalf("webpass npt prefix = %q, want empty for a provider carrying none", got)
	}
}

func TestBuildHealthConfig(t *testing.T) {
	t.Parallel()

	shared := buildWANRefs(sharedWANForTest())
	cfg, err := buildHealthConfig(shared, &config.IfMgrHealthSection{
		StateFile:          "/run/health",
		PersistStateFile:   "/var/lib/health",
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
	})
	if err != nil {
		t.Fatalf("buildHealthConfig returned error: %v", err)
	}
	want := health.Config{
		StateFile:         "/run/health",
		PersistStateFile:  "/var/lib/health",
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
				WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"},
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
				WANRef: ifmgr.WANRef{Name: "webpass", Iface: "webpass0"},
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

// enabledHealthWANSection returns a fully specified, valid enabled health WAN
// section so filter and threshold tests do not trip the enabled-WAN policy
// validation in buildHealthConfig.
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
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := buildHealthConfig(
				buildWANRefs(sharedWANForTest()),
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
		})
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
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := buildHealthConfig(
				buildWANRefs(sharedWANForTest()),
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
		})
	}
}

// TestBuildHealthConfigAcceptsSingleFamilyProvider is the loader half of the
// IPv4-only provider: the configuration loads, and the empty list the provider
// declared reaches the module as an empty list rather than as an omission. The
// module inherits its public ping targets on an omission, so losing the
// distinction here would probe public resolvers out a link with no IPv6.
func TestBuildHealthConfigAcceptsSingleFamilyProvider(t *testing.T) {
	t.Parallel()

	section := enabledHealthWANSection(10)
	section.TargetsV6 = []string{}
	cfg, err := buildHealthConfig(
		buildWANRefs(sharedWANForTest()),
		&config.IfMgrHealthSection{
			WAN: map[string]config.IfMgrHealthWANSection{"att": section},
		},
	)
	if err != nil {
		t.Fatalf("buildHealthConfig rejected an IPv4-only provider: %v", err)
	}
	if len(cfg.WANs) != 1 {
		t.Fatalf("WAN count = %d, want 1", len(cfg.WANs))
	}
	if cfg.WANs[0].TargetsV6 == nil {
		t.Fatal("a declared-empty targets-v6 must reach the module as an empty list, not as nil")
	}
	if len(cfg.WANs[0].TargetsV6) != 0 {
		t.Fatalf("targets-v6 = %v, want empty", cfg.WANs[0].TargetsV6)
	}
	if len(cfg.WANs[0].TargetsV4) != 2 {
		t.Fatalf("targets-v4 count = %d, want 2", len(cfg.WANs[0].TargetsV4))
	}
}

// TestBuildHealthConfigKeepsAnOmittedTargetFamilyNil preserves the inherit:
// a family the provider names no leaf for stays nil, which the module reads as
// "use the module-wide list".
func TestBuildHealthConfigKeepsAnOmittedTargetFamilyNil(t *testing.T) {
	t.Parallel()

	section := enabledHealthWANSection(10)
	section.TargetsV6 = nil
	cfg, err := buildHealthConfig(
		buildWANRefs(sharedWANForTest()),
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
		t.Fatalf("targets-v6 = %v, want nil so the module inherits", cfg.WANs[0].TargetsV6)
	}
}

func TestBuildHealthConfigNilSection(t *testing.T) {
	t.Parallel()

	cfg, err := buildHealthConfig(buildWANRefs(sharedWANForTest()), nil)
	if err != nil {
		t.Fatalf("buildHealthConfig returned error: %v", err)
	}
	if len(cfg.WANs) != 2 {
		t.Fatalf("WAN count = %d, want 2 from the shared list", len(cfg.WANs))
	}
}

// TestBuildIfMgrModuleConfigsWANRoleBuildsAll confirms the wan role yields the
// health, wan.routes, and npt module configs from one shared config.
func TestBuildIfMgrModuleConfigsWANRoleBuildsAll(t *testing.T) {
	t.Parallel()

	modules := config.IfMgrModulesSection{
		Health: &config.IfMgrHealthSection{},
		WAN:    &config.IfMgrModulesWANSection{Routes: &config.IfMgrWANRoutesSection{InternalIface: "enmwanbr0"}},
	}
	set, err := buildIfMgrModuleConfigs(ifmgrForTest(modules), "wan")
	if err != nil {
		t.Fatalf("buildIfMgrModuleConfigs(wan) returned error: %v", err)
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

// TestBuildSteeringConfig pins that the balancer reads the same provider list,
// internal link, and internal network the routing module reads, plus the group
// hash mode the network configuration carries.
func TestBuildSteeringConfig(t *testing.T) {
	t.Parallel()

	ifmgrCfg := sharedWANForTest()
	ifmgrCfg.HashMode = "source"
	shared := buildWANRefs(ifmgrCfg)
	cfg, err := buildSteeringConfig(shared, ifmgrCfg, &config.IfMgrWANRoutesSection{
		InternalIface:   "vmbr250",
		InternalNetV4:   "10.250.250.0/29",
		HealthStateFile: "/var/run/mwan-health.state",
	})
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
			{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}, Mark: 1, Tier: 0, Weight: 1},
			{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "webpass0"}, Mark: 2, Tier: 1, Weight: 3},
		},
	}
	if !reflect.DeepEqual(cfg, want) {
		t.Fatalf("buildSteeringConfig mismatch\ngot:  %#v\nwant: %#v", cfg, want)
	}
}

// TestBuildIfMgrModuleConfigsWANRoleBuildsSteering pins that the wan role builds
// the steering config, so a gateway that runs the role programs the balancer.
func TestBuildIfMgrModuleConfigsWANRoleBuildsSteering(t *testing.T) {
	t.Parallel()

	ifmgrCfg := ifmgrForTest(modulesWithUnresolvableUIDRule())
	ifmgrCfg.HashMode = "random"
	set, err := buildIfMgrModuleConfigs(ifmgrCfg, "wan")
	if err != nil {
		t.Fatalf("buildIfMgrModuleConfigs(wan) returned error: %v", err)
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

// TestNetworkConfigOwnsTheNetworkTree drives the real two-file load path. A
// config.toml that still carries the legacy network keys must contribute none
// of them, and the network tree must reach the module configs from the JSON
// loader's output instead. A render-versus-schema mismatch that struct-built
// fixtures cannot catch fails here rather than in production.
func TestNetworkConfigOwnsTheNetworkTree(t *testing.T) {
	t.Parallel()

	// Every network key below is one a pre-cutover config.toml carries. None of
	// them may reach the parsed config.
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
		t.Fatalf("internal_iface still decodes from TOML: %q",
			cfg.IfMgr.Modules.WAN.Routes.InternalIface)
	}
	if len(cfg.IfMgr.Modules.Health.WAN) != 0 {
		t.Fatalf("[ifmgr.modules.health.wan] still decodes from TOML: %#v",
			cfg.IfMgr.Modules.Health.WAN)
	}
	// The two paths the network file deliberately does not carry must survive.
	if cfg.IfMgr.Modules.Health.StateFile != "/run/mwan-health.state" {
		t.Fatalf("health state_file did not parse: %q", cfg.IfMgr.Modules.Health.StateFile)
	}
	if cfg.IfMgr.Modules.WAN.Routes.HealthStateFile != "/run/mwan-health.state" {
		t.Fatalf("wan.routes health_state_file did not parse: %q",
			cfg.IfMgr.Modules.WAN.Routes.HealthStateFile)
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
				Iface:      "enatt0",
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:2300::/60",
				V4Source:   "",
			},
			"webpass": {
				Iface:      "enwebpass0",
				TableID:    200,
				FwMark:     2,
				FwMarkPrio: 200,
				FromPrio:   56,
				NptPrefix:  "3d06:bad:b01:2200::/60",
				V4Source:   "10.241.204.2",
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

	set, err := buildIfMgrModuleConfigs(cfg.IfMgr, "wan")
	if err != nil {
		t.Fatalf("buildIfMgrModuleConfigs(wan): %v", err)
	}
	wr, ok := set["wan.routes"].(wanroutes.Config)
	if !ok {
		t.Fatalf("wan.routes config missing or wrong type: %T", set["wan.routes"])
	}
	if wr.InternalIface != "enmwanbr0" {
		t.Fatalf("wan.routes internal iface = %q, want enmwanbr0", wr.InternalIface)
	}
	if wr.HealthStateFile != "/run/mwan-health.state" {
		t.Fatalf("wan.routes health state file = %q, want /run/mwan-health.state",
			wr.HealthStateFile)
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

// TestGatewayLoadWithoutNetworkTOML is the end state: the gateway's config.toml
// carries no network section at all, and the wan role still builds every module
// config from the network file plus the two state-file paths TOML keeps.
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
persist_state_file = "/var/lib/mwan/health-state"
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
				Iface:      "enatt0",
				TableID:    100,
				FwMark:     1,
				FwMarkPrio: 100,
				FromPrio:   55,
				NptPrefix:  "3d06:bad:b01:2300::/60",
				V4Source:   "",
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

	set, err := buildIfMgrModuleConfigs(cfg.IfMgr, "wan")
	if err != nil {
		t.Fatalf("buildIfMgrModuleConfigs(wan): %v", err)
	}
	nptConfig, ok := set["npt"].(npt.Config)
	if !ok {
		t.Fatalf("npt config missing or wrong type: %T", set["npt"])
	}
	if nptConfig.InternalPrefix != "3d06:bad:b01:210::/60" {
		t.Fatalf("npt internal prefix = %q, want the network file's value",
			nptConfig.InternalPrefix)
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
	if hc.PersistStateFile != "/var/lib/mwan/health-state" {
		t.Fatalf("health persist state file = %q, want TOML's value", hc.PersistStateFile)
	}
	if len(hc.WANs) != 1 || hc.WANs[0].CheckInterval != 10*time.Second {
		t.Fatalf("health WAN list did not resolve: %#v", hc.WANs)
	}
}
