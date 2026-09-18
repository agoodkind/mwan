package health

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/statuspush"
	"goodkind.io/mwan/internal/wanstate"
)

func TestAdvanceHealthAppliesConsecutiveThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		cycles            []bool
		failureThreshold  int
		recoveryThreshold int
		want              []State
	}{
		{
			name:              "unknown becomes healthy after consecutive successes",
			cycles:            []bool{true, true},
			failureThreshold:  2,
			recoveryThreshold: 2,
			want:              []State{StateUnknown, StateHealthy},
		},
		{
			name:              "unknown becomes unhealthy after consecutive failures",
			cycles:            []bool{false, false},
			failureThreshold:  2,
			recoveryThreshold: 2,
			want:              []State{StateUnknown, StateUnhealthy},
		},
		{
			name:              "opposite result resets the active counter",
			cycles:            []bool{true, false, true, true, false, false},
			failureThreshold:  2,
			recoveryThreshold: 2,
			want: []State{
				StateUnknown,
				StateUnknown,
				StateUnknown,
				StateHealthy,
				StateHealthy,
				StateUnhealthy,
			},
		},
		{
			name:              "healthy waits for recovery threshold after failure",
			cycles:            []bool{false, false, true, true, true},
			failureThreshold:  2,
			recoveryThreshold: 3,
			want: []State{
				StateUnknown,
				StateUnhealthy,
				StateUnhealthy,
				StateUnhealthy,
				StateHealthy,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			status := wanStatus{State: StateUnknown}
			got := make([]State, 0, len(test.cycles))
			for _, passed := range test.cycles {
				status, _ = advanceHealth(
					status,
					passed,
					test.failureThreshold,
					test.recoveryThreshold,
				)
				got = append(got, status.State)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("states = %v, want %v", got, test.want)
			}
		})
	}
}

func TestAdvanceHealthUsesResolvedPerWANThresholds(t *testing.T) {
	t.Parallel()

	cfg := Config{
		FailureThreshold:  2,
		RecoveryThreshold: 2,
	}
	wan := WAN{
		FailureThreshold:  5,
		RecoveryThreshold: 3,
	}
	status := wanStatus{State: StateUnknown}
	for range 4 {
		status, _ = advanceHealth(
			status,
			false,
			wan.failureThreshold(cfg),
			wan.recoveryThreshold(cfg),
		)
	}
	if status.State != StateUnknown {
		t.Fatalf("state after 4 failures = %s, want %s", status.State, StateUnknown)
	}
	status, _ = advanceHealth(
		status,
		false,
		wan.failureThreshold(cfg),
		wan.recoveryThreshold(cfg),
	)
	if status.State != StateUnhealthy {
		t.Fatalf("state after 5 failures = %s, want %s", status.State, StateUnhealthy)
	}
	for range 2 {
		status, _ = advanceHealth(
			status,
			true,
			wan.failureThreshold(cfg),
			wan.recoveryThreshold(cfg),
		)
	}
	if status.State != StateUnhealthy {
		t.Fatalf("state after 2 recoveries = %s, want %s", status.State, StateUnhealthy)
	}
	status, _ = advanceHealth(
		status,
		true,
		wan.failureThreshold(cfg),
		wan.recoveryThreshold(cfg),
	)
	if status.State != StateHealthy {
		t.Fatalf("state after 3 recoveries = %s, want %s", status.State, StateHealthy)
	}
}

func TestWANResolversFallBackToModuleConfig(t *testing.T) {
	t.Parallel()

	cfg := Config{
		TargetsV4:         []netip.Addr{netip.MustParseAddr("192.0.2.1")},
		TargetsV6:         []netip.Addr{netip.MustParseAddr("2001:db8::1")},
		HTTPURLs:          []string{"https://example.test/health"},
		PingCount:         3,
		SuccessThreshold:  2,
		FailureThreshold:  4,
		RecoveryThreshold: 5,
	}
	wan := WAN{}
	if !reflect.DeepEqual(wan.targetsV4(cfg), cfg.TargetsV4) {
		t.Fatal("targetsV4 did not fall back to the module config")
	}
	if !reflect.DeepEqual(wan.targetsV6(cfg), cfg.TargetsV6) {
		t.Fatal("targetsV6 did not fall back to the module config")
	}
	if !reflect.DeepEqual(wan.httpURLs(cfg), cfg.HTTPURLs) {
		t.Fatal("httpURLs did not fall back to the module config")
	}
	if wan.pingCount(cfg) != cfg.PingCount {
		t.Fatal("pingCount did not fall back to the module config")
	}
	if wan.successThreshold(cfg) != cfg.SuccessThreshold {
		t.Fatal("successThreshold did not fall back to the module config")
	}
	if wan.failureThreshold(cfg) != cfg.FailureThreshold {
		t.Fatal("failureThreshold did not fall back to the module config")
	}
	if wan.recoveryThreshold(cfg) != cfg.RecoveryThreshold {
		t.Fatal("recoveryThreshold did not fall back to the module config")
	}
}

// TestWANResolversDistinguishAnEmptyTargetListFromAnOmittedOne proves the
// distinction an IPv4-only provider rests on: a family the provider left out
// inherits the module-wide list, and a family it declared empty does not, so
// the module never probes public resolvers out a link that has no IPv6.
func TestWANResolversDistinguishAnEmptyTargetListFromAnOmittedOne(t *testing.T) {
	t.Parallel()

	cfg := Config{
		TargetsV4: []netip.Addr{netip.MustParseAddr("192.0.2.1")},
		TargetsV6: []netip.Addr{netip.MustParseAddr("2001:db8::1")},
	}
	wan := WAN{TargetsV6: []netip.Addr{}}
	if got := wan.targetsV6(cfg); len(got) != 0 {
		t.Fatalf("targetsV6 = %v, want none for a declared-empty list", got)
	}
	if !reflect.DeepEqual(wan.targetsV4(cfg), cfg.TargetsV4) {
		t.Fatal("an omitted targetsV4 must still inherit the module-wide list")
	}
}

func TestValidateConfigUsesResolvedPerWANPolicy(t *testing.T) {
	t.Parallel()

	base := Config{
		StateFile:        "/run/health",
		PersistStateFile: "/var/lib/health",
		TargetsV4: []netip.Addr{
			netip.MustParseAddr("192.0.2.1"),
			netip.MustParseAddr("192.0.2.2"),
		},
		TargetsV6: []netip.Addr{
			netip.MustParseAddr("2001:db8::1"),
			netip.MustParseAddr("2001:db8::2"),
		},
		HTTPURLs:          nil,
		Timeout:           time.Second,
		Interval:          10 * time.Second,
		PingCount:         3,
		SuccessThreshold:  2,
		FailureThreshold:  2,
		RecoveryThreshold: 2,
		WANs:              nil,
	}
	tests := []struct {
		name      string
		wan       WAN
		wantError string
	}{
		{
			name: "valid overrides",
			wan: WAN{
				WANRef:            ifmgr.WANRef{Name: "monkeybrains", Iface: "mbrains0"},
				TargetsV4:         []netip.Addr{netip.MustParseAddr("198.51.100.1")},
				TargetsV6:         []netip.Addr{netip.MustParseAddr("2001:db8:1::1")},
				HTTPURLs:          nil,
				PingCount:         5,
				SuccessThreshold:  1,
				FailureThreshold:  5,
				RecoveryThreshold: 3,
				CheckInterval:     30 * time.Second,
			},
			wantError: "",
		},
		{
			name: "success threshold exceeds IPv6 targets",
			wan: WAN{
				WANRef:           ifmgr.WANRef{Name: "att", Iface: "att0"},
				TargetsV6:        []netip.Addr{netip.MustParseAddr("2001:db8::1")},
				SuccessThreshold: 2,
			},
			wantError: "success_threshold exceeds an address-family target count",
		},
		{
			name: "IPv4-only provider is valid",
			wan: WAN{
				WANRef: ifmgr.WANRef{Name: "astound", Iface: "astound0"},
				TargetsV4: []netip.Addr{
					netip.MustParseAddr("198.51.100.1"),
					netip.MustParseAddr("198.51.100.2"),
				},
				TargetsV6:        []netip.Addr{},
				SuccessThreshold: 2,
			},
			wantError: "",
		},
		{
			name: "no targets in either family",
			wan: WAN{
				WANRef:    ifmgr.WANRef{Name: "astound", Iface: "astound0"},
				TargetsV4: []netip.Addr{},
				TargetsV6: []netip.Addr{},
			},
			wantError: "at least one targets_v4 or targets_v6 entry is required",
		},
		{
			name: "negative ping count",
			wan: WAN{
				WANRef:    ifmgr.WANRef{Name: "att", Iface: "att0"},
				PingCount: -1,
			},
			wantError: "ping_count must be > 0",
		},
		{
			name: "negative failure threshold",
			wan: WAN{
				WANRef:           ifmgr.WANRef{Name: "att", Iface: "att0"},
				FailureThreshold: -1,
			},
			wantError: "failure_threshold must be > 0",
		},
		{
			name: "negative recovery threshold",
			wan: WAN{
				WANRef:            ifmgr.WANRef{Name: "att", Iface: "att0"},
				RecoveryThreshold: -1,
			},
			wantError: "recovery_threshold must be > 0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cfg := base
			cfg.WANs = []WAN{test.wan}
			err := validateConfig(cfg)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("validateConfig returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("validateConfig error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestProbeWANVerdictIsV6OrV4AndAlwaysProbesBoth(t *testing.T) {
	t.Parallel()

	// The verdict matches health-check.sh: healthy when IPv6 meets the
	// threshold (preferred) or IPv4 meets it (fallback). Both families of a
	// provider that carries both are probed every cycle regardless of outcome,
	// so a v4 flap never fails a healthy-v6 WAN and a v6 outage can still fall
	// back to v4.
	tests := []struct {
		name     string
		v6Passes bool
		v4Passes bool
		wantPass bool
	}{
		{
			name:     "IPv6 up keeps the WAN healthy when IPv4 is down",
			v6Passes: true,
			v4Passes: false,
			wantPass: true,
		},
		{
			name:     "IPv6 down falls back to IPv4",
			v6Passes: false,
			v4Passes: true,
			wantPass: true,
		},
		{
			name:     "both families down is unhealthy",
			v6Passes: false,
			v4Passes: false,
			wantPass: false,
		},
		{
			name:     "both families up is healthy",
			v6Passes: true,
			v4Passes: true,
			wantPass: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var calls []string
			module := testProbeModule(
				func(
					_ context.Context,
					_ string,
					_ netip.Addr,
					_ time.Duration,
				) (time.Duration, error) {
					calls = append(calls, "v6")
					if test.v6Passes {
						return time.Millisecond, nil
					}
					return 0, errors.New("IPv6 unavailable")
				},
				func(
					_ context.Context,
					_ string,
					_ netip.Addr,
					_ time.Duration,
				) (time.Duration, error) {
					calls = append(calls, "v4")
					if test.v4Passes {
						return time.Millisecond, nil
					}
					return 0, errors.New("IPv4 unavailable")
				},
			)

			result := module.probeWAN(
				context.Background(),
				module.cfg.WANs[0],
				slog.New(slog.NewTextHandler(io.Discard, nil)),
			)
			if result.Passed != test.wantPass {
				t.Fatalf("Passed = %t, want %t", result.Passed, test.wantPass)
			}
			wantCalls := []string{"v6", "v6", "v4", "v4"}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("probe calls = %v, want %v", calls, wantCalls)
			}
		})
	}
}

// TestIPv4OnlyProviderValidatesProbesOneFamilyAndReadsHealthy is the contract
// for a provider on a link with no IPv6: the configuration validates, the cycle
// issues no IPv6 probe of either kind, the verdict rests on the IPv4 leg, and
// the management surface reports the absent family as unprobed rather than as
// failing. The module-wide lists carry the public resolvers applyDefaults
// installs, so an inherited IPv6 list would show up as an IPv6 probe here.
func TestIPv4OnlyProviderValidatesProbesOneFamilyAndReadsHealthy(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	var probed []string
	recordPing := func(family string, reachable bool) pingFunc {
		return func(
			_ context.Context,
			_ string,
			_ netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			probed = append(probed, family)
			if reachable {
				return time.Millisecond, nil
			}
			return 0, errors.New(family + " unavailable")
		}
	}
	recordHTTP := func(family string) httpFunc {
		return func(_ context.Context, _ string, _ string, _ time.Duration) (int, error) {
			probed = append(probed, family)
			return 200, nil
		}
	}
	cfg := Config{
		StateFile:         filepath.Join(tempDir, "mwan-health.state"),
		PersistStateFile:  filepath.Join(tempDir, "health-state"),
		TargetsV4:         defaultTargetsV4(),
		TargetsV6:         defaultTargetsV6(),
		HTTPURLs:          []string{"https://example.test/probe"},
		Timeout:           time.Second,
		Interval:          10 * time.Second,
		PingCount:         1,
		SuccessThreshold:  1,
		FailureThreshold:  1,
		RecoveryThreshold: 1,
		WANs: []WAN{
			{
				WANRef:    ifmgr.WANRef{Name: "astound", Iface: "enastound0"},
				TargetsV4: []netip.Addr{netip.MustParseAddr("192.0.2.1")},
				// Declared empty: this provider carries no IPv6 at all.
				TargetsV6: []netip.Addr{},
			},
		},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig rejected an IPv4-only provider: %v", err)
	}

	store := wanstate.New()
	module := &Module{
		BaseModule: ifmgr.NewBaseModule(moduleName),
		cfg:        cfg,
		clock:      fixedClock{now: time.Date(2026, time.September, 16, 0, 0, 0, 0, time.UTC)},
		statuses:   map[string]wanStatus{"astound": {State: StateUnknown}},
		probeV6:    recordPing("ping6", false),
		probeV4:    recordPing("ping4", true),
		probeHTTP6: recordHTTP("http6"),
		probeHTTP4: recordHTTP("http4"),
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	module.InitBase(&ifmgr.Env{Log: log, LiveState: store}, "module", moduleName)

	if err := module.runCycle(context.Background(), log); err != nil {
		t.Fatalf("runCycle: %v", err)
	}

	wantProbes := []string{"ping4", "http4"}
	if !reflect.DeepEqual(probed, wantProbes) {
		t.Fatalf("probes issued = %v, want %v", probed, wantProbes)
	}
	contents, err := os.ReadFile(cfg.StateFile)
	if err != nil {
		t.Fatalf("ReadFile(state): %v", err)
	}
	if string(contents) != "astound:healthy\n" {
		t.Fatalf("state file = %q, want %q", contents, "astound:healthy\n")
	}
	member := store.Snapshot().Health["astound"]
	if member.Verdict != wanstate.HealthHealthy {
		t.Fatalf("verdict = %q, want %q", member.Verdict, wanstate.HealthHealthy)
	}
	if member.V4 != wanstate.ProbePass {
		t.Fatalf("IPv4 leg = %q, want %q", member.V4, wanstate.ProbePass)
	}
	if member.V6 != wanstate.ProbeNone {
		t.Fatalf(
			"IPv6 leg = %q, want %q for a provider that carries no IPv6",
			member.V6, wanstate.ProbeNone,
		)
	}
}

func TestProbeWANUsesPerWANSuccessThreshold(t *testing.T) {
	t.Parallel()

	v6PassingTarget := netip.MustParseAddr("2001:db8::1")
	v4PassingTarget := netip.MustParseAddr("192.0.2.1")
	module := testProbeModule(
		func(
			_ context.Context,
			_ string,
			target netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			if target == v6PassingTarget {
				return time.Millisecond, nil
			}
			return 0, errors.New("IPv6 target unavailable")
		},
		func(
			_ context.Context,
			_ string,
			target netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			if target == v4PassingTarget {
				return time.Millisecond, nil
			}
			return 0, errors.New("IPv4 target unavailable")
		},
	)
	module.cfg.WANs = []WAN{
		{
			WANRef:           ifmgr.WANRef{Name: "monkeybrains", Iface: "mbrains0"},
			SuccessThreshold: 1,
		},
		{
			WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"},
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name     string
		wan      WAN
		wantPass bool
	}{
		{
			name:     "per-WAN threshold accepts one target",
			wan:      module.cfg.WANs[0],
			wantPass: true,
		},
		{
			name:     "module threshold still requires two targets",
			wan:      module.cfg.WANs[1],
			wantPass: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result := module.probeWAN(context.Background(), test.wan, log)
			if result.Passed != test.wantPass {
				t.Fatalf("Passed = %t, want %t", result.Passed, test.wantPass)
			}
		})
	}
}

func TestProbeWANHTTPSucceedsWhenBothPingFamiliesFail(t *testing.T) {
	t.Parallel()

	pingFail := func(
		_ context.Context,
		_ string,
		_ netip.Addr,
		_ time.Duration,
	) (time.Duration, error) {
		return 0, errors.New("ping unavailable")
	}
	httpOK := func(
		_ context.Context,
		_ string,
		_ string,
		_ time.Duration,
	) (int, error) {
		return 200, nil
	}
	httpFail := func(
		_ context.Context,
		_ string,
		_ string,
		_ time.Duration,
	) (int, error) {
		return 0, errors.New("http unavailable")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	cases := []struct {
		name     string
		probe6   httpFunc
		probe4   httpFunc
		wantPass bool
	}{
		{name: "http6 alone keeps the WAN healthy", probe6: httpOK, probe4: httpFail, wantPass: true},
		{name: "http4 alone keeps the WAN healthy", probe6: httpFail, probe4: httpOK, wantPass: true},
		{name: "both HTTP families failing marks the cycle failed", probe6: httpFail, probe4: httpFail, wantPass: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			module := testProbeModule(pingFail, pingFail)
			module.cfg.HTTPURLs = []string{"http://example.test/probe"}
			module.probeHTTP6 = testCase.probe6
			module.probeHTTP4 = testCase.probe4
			result := module.probeWAN(context.Background(), module.cfg.WANs[0], log)
			if result.Passed != testCase.wantPass {
				t.Fatalf("Passed = %t, want %t", result.Passed, testCase.wantPass)
			}
		})
	}
}

func TestProbeTargetsUsesEveryConfiguredPing(t *testing.T) {
	t.Parallel()

	callCount := 0
	module := testProbeModule(
		func(
			_ context.Context,
			_ string,
			_ netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			callCount++
			return time.Millisecond, nil
		},
		func(
			_ context.Context,
			_ string,
			_ netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			return time.Millisecond, nil
		},
	)
	module.cfg.TargetsV6 = module.cfg.TargetsV6[:1]
	module.cfg.PingCount = 3

	successes := module.probeTargets(
		context.Background(),
		module.cfg.WANs[0],
		module.cfg.TargetsV6,
		module.probeV6,
	)
	if successes != 1 {
		t.Fatalf("successes = %d, want 1", successes)
	}
	if callCount != 3 {
		t.Fatalf("probe call count = %d, want 3", callCount)
	}
}

func TestWriteStateFilesUsesShellFormat(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	stateFile := filepath.Join(tempDir, "run", "mwan-health.state")
	persistStateFile := filepath.Join(tempDir, "lib", "health-state")
	module := &Module{
		cfg: Config{
			StateFile:        stateFile,
			PersistStateFile: persistStateFile,
			WANs: []WAN{
				{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}},
				{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "webpass0"}},
			},
		},
		statuses: map[string]wanStatus{
			"att":     {State: StateHealthy},
			"webpass": {State: StateUnhealthy},
		},
	}

	if err := module.writeStateFiles(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		module.statuses,
	); err != nil {
		t.Fatalf("writeStateFiles: %v", err)
	}

	wantContents := "att:healthy\nwebpass:unhealthy\n"
	for _, path := range []string{stateFile, persistStateFile} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%q): %v", path, err)
		}
		if string(contents) != wantContents {
			t.Fatalf("%s contents = %q, want %q", path, contents, wantContents)
		}
	}
}

func TestWriteStateFilesToleratesPersistFailure(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	blocker := filepath.Join(tempDir, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}
	statePath := filepath.Join(tempDir, "run", "mwan-health.state")
	module := &Module{
		cfg: Config{
			StateFile:        statePath,
			PersistStateFile: filepath.Join(blocker, "health-state"),
			WANs:             []WAN{{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}}},
		},
		statuses: map[string]wanStatus{"att": {State: StateHealthy}},
	}

	// The persist mirror lives under an unwritable path, mirroring the ifmgr
	// sandbox where /var/lib is read-only. The runtime write must still succeed
	// and the call must not error.
	if err := module.writeStateFiles(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		module.statuses,
	); err != nil {
		t.Fatalf("writeStateFiles must tolerate a persist failure: %v", err)
	}
	contents, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("runtime state not written: %v", err)
	}
	if string(contents) != "att:healthy\n" {
		t.Fatalf("runtime state = %q, want %q", contents, "att:healthy\n")
	}
}

func TestInitFailsWhenRuntimeStateUnwritable(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	blocker := filepath.Join(tempDir, "blocked")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}
	module := &Module{
		cfg: Config{
			StateFile:        filepath.Join(blocker, "mwan-health.state"),
			PersistStateFile: filepath.Join(tempDir, "health-state"),
			TargetsV6: []netip.Addr{
				netip.MustParseAddr("2001:db8::1"),
				netip.MustParseAddr("2001:db8::2"),
			},
			TargetsV4: []netip.Addr{
				netip.MustParseAddr("192.0.2.1"),
				netip.MustParseAddr("192.0.2.2"),
			},
			Timeout:           time.Second,
			Interval:          10 * time.Second,
			PingCount:         1,
			SuccessThreshold:  1,
			FailureThreshold:  1,
			RecoveryThreshold: 1,
			WANs:              []WAN{{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}}},
		},
	}
	env := &ifmgr.Env{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}

	// The runtime state file lives under an unwritable path. The runtime output
	// is required, so Init must fail rather than start the loop with no state
	// file. A persist-only failure is tolerated separately (see the persist
	// test), so this asserts the runtime path stays load-bearing.
	if err := module.Init(context.Background(), env); err == nil {
		t.Fatal("Init must fail when the runtime state file cannot be written")
	}
}

// fakeStatusSender records every push so the test asserts on what the module
// actually put on the wire. The real Sender is exercised end to end in the
// statuspush package's own round-trip test; here the question is what the
// health module sends and when.
type fakeStatusSender struct {
	mu   sync.Mutex
	sent []statuspush.Status
}

func (f *fakeStatusSender) Send(_ context.Context, status statuspush.Status) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, status)
}

func (f *fakeStatusSender) all() []statuspush.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]statuspush.Status(nil), f.sent...)
}

// TestPushSendsOncePerCycleAndCarriesTheTransition is the contract the watchdog
// depends on: every probe cycle puts one status on the wire, and the cycle that
// flips a provider to unhealthy carries that verdict and the tier that took
// over. Two providers, att alone in tier 0 and monkeybrains alone in tier 1, so
// an att failure moves the active tier.
func TestPushSendsOncePerCycleAndCarriesTheTransition(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	pusher := &fakeStatusSender{}
	failing := func(
		_ context.Context, iface string, _ netip.Addr, _ time.Duration,
	) (time.Duration, error) {
		if iface == "enatt0" {
			return 0, errors.New("att is down")
		}
		return time.Millisecond, nil
	}
	noHTTP := func(
		_ context.Context, _ string, _ string, _ time.Duration,
	) (int, error) {
		return 0, errors.New("no http")
	}
	module := &Module{
		BaseModule: ifmgr.NewBaseModule("health"),
		cfg: Config{
			StateFile:         filepath.Join(tempDir, "mwan-health.state"),
			PersistStateFile:  filepath.Join(tempDir, "health-state"),
			TargetsV4:         []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			TargetsV6:         []netip.Addr{netip.MustParseAddr("2001:db8:53::1")},
			Timeout:           time.Second,
			Interval:          time.Second,
			PingCount:         1,
			SuccessThreshold:  1,
			FailureThreshold:  1,
			RecoveryThreshold: 1,
			StatusPushCID:     statuspush.HostCID,
			StatusPushPort:    statuspush.DefaultPort,
			WANs: []WAN{
				{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0"}, Tier: 0},
				{WANRef: ifmgr.WANRef{Name: "monkeybrains", Iface: "enmbrains0"}, Tier: 1},
			},
		},
		statuses: map[string]wanStatus{
			"att":          {State: StateHealthy},
			"monkeybrains": {State: StateHealthy},
		},
		probeV4:    failing,
		probeV6:    failing,
		probeHTTP6: noHTTP,
		probeHTTP4: noHTTP,
		pusher:     pusher,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := module.runCycle(context.Background(), log); err != nil {
		t.Fatalf("first runCycle: %v", err)
	}
	if got := len(pusher.all()); got != 1 {
		t.Fatalf("pushes after one cycle = %d, want exactly 1", got)
	}
	first := pusher.all()[0]
	if first.Providers["att"] != string(StateUnhealthy) {
		t.Fatalf("att verdict = %q, want unhealthy", first.Providers["att"])
	}
	if first.Providers["monkeybrains"] != string(StateHealthy) {
		t.Fatalf("monkeybrains verdict = %q, want healthy",
			first.Providers["monkeybrains"])
	}
	if first.ActiveTier != 1 {
		t.Fatalf("active tier = %d, want 1 after att failed", first.ActiveTier)
	}
	if first.SentAt.IsZero() {
		t.Fatal("sent_at is zero")
	}

	// A steady cycle with no transition still reports, because the watchdog
	// reads the age of the last status and a silent gateway must look silent.
	if err := module.runCycle(context.Background(), log); err != nil {
		t.Fatalf("second runCycle: %v", err)
	}
	if got := len(pusher.all()); got != 2 {
		t.Fatalf("pushes after two cycles = %d, want 2", got)
	}
}

// TestPushIsSkippedWhenUnconfigured proves a gateway with no push address does
// not build a sender and does not fail a cycle for the lack of one.
func TestPushIsSkippedWhenUnconfigured(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	passing := func(
		_ context.Context, _ string, _ netip.Addr, _ time.Duration,
	) (time.Duration, error) {
		return time.Millisecond, nil
	}
	module := &Module{
		BaseModule: ifmgr.NewBaseModule("health"),
		cfg: Config{
			StateFile:         filepath.Join(tempDir, "mwan-health.state"),
			PersistStateFile:  filepath.Join(tempDir, "health-state"),
			TargetsV4:         []netip.Addr{netip.MustParseAddr("192.0.2.10")},
			TargetsV6:         []netip.Addr{netip.MustParseAddr("2001:db8:53::1")},
			Timeout:           time.Second,
			Interval:          time.Second,
			PingCount:         1,
			SuccessThreshold:  1,
			FailureThreshold:  1,
			RecoveryThreshold: 1,
			WANs: []WAN{
				{WANRef: ifmgr.WANRef{Name: "att", Iface: "enatt0"}, Tier: 0},
			},
		},
		statuses: map[string]wanStatus{"att": {State: StateUnknown}},
		probeV4:  passing,
		probeV6:  passing,
		pusher:   nil,
	}

	if err := module.runCycle(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	); err != nil {
		t.Fatalf("runCycle with no pusher: %v", err)
	}
}

func TestRunCycleDoesNotCommitStateWhenPublicationFails(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	blockedParent := filepath.Join(tempDir, "blocked")
	if err := os.WriteFile(blockedParent, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile blocker: %v", err)
	}
	module := testProbeModule(
		func(
			_ context.Context,
			_ string,
			_ netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			return 0, errors.New("IPv6 unavailable")
		},
		func(
			_ context.Context,
			_ string,
			_ netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			return 0, errors.New("IPv4 unavailable")
		},
	)
	module.cfg.StateFile = filepath.Join(blockedParent, "mwan-health.state")
	module.cfg.PersistStateFile = filepath.Join(tempDir, "health-state")
	module.cfg.FailureThreshold = 1
	module.cfg.RecoveryThreshold = 1
	module.statuses = map[string]wanStatus{
		"att": {State: StateHealthy},
	}

	err := module.runCycle(
		context.Background(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err == nil {
		t.Fatal("runCycle must fail when the runtime state cannot be written")
	}
	if got := module.statuses["att"].State; got != StateHealthy {
		t.Fatalf("state after failed publication = %s, want %s", got, StateHealthy)
	}
}

func TestReconcileRunsOnlyTheStartupProbe(t *testing.T) {
	t.Parallel()

	callCount := 0
	probe := func(
		_ context.Context,
		_ string,
		_ netip.Addr,
		_ time.Duration,
	) (time.Duration, error) {
		callCount++
		return time.Millisecond, nil
	}
	module := testProbeModule(probe, probe)
	tempDir := t.TempDir()
	module.cfg.StateFile = filepath.Join(tempDir, "mwan-health.state")
	module.cfg.PersistStateFile = filepath.Join(tempDir, "health-state")
	module.cfg.FailureThreshold = 1
	module.cfg.RecoveryThreshold = 1
	module.statuses = map[string]wanStatus{
		"att": {State: StateUnknown},
	}
	module.reconcilePending = true
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := module.Reconcile(context.Background(), log); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if err := module.Reconcile(context.Background(), log); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if callCount != 4 {
		t.Fatalf("probe call count = %d, want 4 from one dual-family cycle", callCount)
	}
}

func TestEmitTransitionsUsesInjectedClock(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	notifier := &recordingNotifier{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	module := &Module{
		BaseModule: ifmgr.NewBaseModule(moduleName),
		cfg:        Config{},
		clock:      fixedClock{now: now},
	}
	module.InitBase(&ifmgr.Env{
		Iface:   "",
		Sysctl:  nil,
		Log:     log,
		Alerts:  ifmgr.WrapNotifier(notifier),
		Monitor: nil,
		DHCP:    nil,
		RA:      nil,
	}, "module", moduleName)

	module.emitTransitions(context.Background(), log, []transition{
		{
			WAN:  WAN{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}},
			From: StateHealthy,
			To:   StateUnhealthy,
		},
	})

	if len(notifier.events) != 1 {
		t.Fatalf("Notify event count = %d, want 1", len(notifier.events))
	}
	if notifier.events[0].Now != now {
		t.Fatalf("Notify event time = %s, want %s", notifier.events[0].Now, now)
	}
}

func TestEmitTransitionAlertsFromUnknownUnhealthy(t *testing.T) {
	t.Parallel()

	notifier := &recordingNotifier{}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	module := &Module{
		BaseModule: ifmgr.NewBaseModule(moduleName),
		cfg:        Config{},
		clock:      fixedClock{now: time.Date(2026, time.July, 24, 0, 0, 0, 0, time.UTC)},
	}
	module.InitBase(&ifmgr.Env{Log: log, Alerts: ifmgr.WrapNotifier(notifier)}, "module", moduleName)

	// A WAN broken from startup goes unknown -> unhealthy and must alert.
	module.emitTransitions(context.Background(), log, []transition{
		{WAN: WAN{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}}, From: StateUnknown, To: StateUnhealthy},
	})
	if len(notifier.events) != 1 {
		t.Fatalf("unknown->unhealthy Notify count = %d, want 1", len(notifier.events))
	}

	// A WAN that comes up healthy from unknown has no prior alert to resolve.
	module.emitTransitions(context.Background(), log, []transition{
		{WAN: WAN{WANRef: ifmgr.WANRef{Name: "webpass", Iface: "webpass0"}}, From: StateUnknown, To: StateHealthy},
	})
	if len(notifier.events) != 1 {
		t.Fatalf("unknown->healthy must not emit a Notify; count = %d, want 1", len(notifier.events))
	}
}

func TestEmitTransitionRequestsReconcile(t *testing.T) {
	t.Parallel()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var reconcileReasons []string
	module := &Module{
		BaseModule: ifmgr.NewBaseModule(moduleName),
		cfg:        Config{},
		clock:      fixedClock{now: time.Date(2026, time.July, 24, 0, 0, 0, 0, time.UTC)},
	}
	module.InitBase(&ifmgr.Env{
		Log:    log,
		Alerts: ifmgr.WrapNotifier(&recordingNotifier{}),
		RequestReconcile: func(reason string) {
			reconcileReasons = append(reconcileReasons, reason)
		},
	}, "module", moduleName)

	// A health transition must ask the daemon to reconcile now so wan.routes
	// fails over event-driven, not on the tick.
	module.emitTransitions(context.Background(), log, []transition{
		{
			WAN:  WAN{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}},
			From: StateHealthy,
			To:   StateUnhealthy,
		},
	})

	if len(reconcileReasons) != 1 {
		t.Fatalf("RequestReconcile calls = %d, want 1", len(reconcileReasons))
	}
}

func testProbeModule(probeV6 pingFunc, probeV4 pingFunc) *Module {
	return &Module{
		cfg: Config{
			TargetsV6: []netip.Addr{
				netip.MustParseAddr("2001:db8::1"),
				netip.MustParseAddr("2001:db8::2"),
			},
			TargetsV4: []netip.Addr{
				netip.MustParseAddr("192.0.2.1"),
				netip.MustParseAddr("192.0.2.2"),
			},
			Timeout:          time.Second,
			PingCount:        1,
			SuccessThreshold: 2,
			WANs: []WAN{
				{WANRef: ifmgr.WANRef{Name: "att", Iface: "att0"}},
			},
		},
		probeV6: probeV6,
		probeV4: probeV4,
	}
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

type recordingNotifier struct {
	events []notify.Event
}

func (n *recordingNotifier) Notify(_ context.Context, event notify.Event) {
	n.events = append(n.events, event)
}

func (n *recordingNotifier) Resolve(
	_ context.Context,
	_,
	_,
	_ string,
	_ ...slog.Attr,
) {
}

func (n *recordingNotifier) Active(_, _ string) bool {
	return len(n.events) > 0
}
