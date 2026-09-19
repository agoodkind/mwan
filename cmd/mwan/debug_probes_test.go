package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/netip"
	"reflect"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/npt"
	"goodkind.io/mwan/internal/netif"
)

type debugProbeTestCall struct {
	kind   string
	iface  string
	family string
	target string
}

type fakeDebugPrefixSource struct {
	calls    []string
	prefixes map[string]netip.Prefix
	errors   map[string]error
}

func (source *fakeDebugPrefixSource) Prefix(
	_ context.Context,
	iface string,
) (netip.Prefix, bool, error) {
	source.calls = append(source.calls, iface)
	if err := source.errors[iface]; err != nil {
		return netip.Prefix{}, false, err
	}
	prefix, ok := source.prefixes[iface]
	return prefix, ok, nil
}

func TestDebugConnectivityRendersOrderedWANStateAndSequentialProbes(t *testing.T) {
	t.Parallel()

	cfg := debugProbeTestConfig()
	var calls []debugProbeTestCall
	listCalls := make([]string, 0, 3)
	renderCount := 0
	pingOutcomes := map[string][]error{
		"ping4|enatt0":         {errors.New("lost"), nil},
		"ping6|enatt0":         {errors.New("lost"), errors.New("lost")},
		"ping4|enwebpass0":     {errors.New("lost"), errors.New("lost")},
		"ping6|enmonkeybrains": {nil, errors.New("lost")},
	}
	dependencies := debugProbeDependencies{
		ping4: func(
			_ context.Context,
			iface string,
			target netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			calls = append(calls, debugProbeTestCall{
				kind:   "ping4",
				iface:  iface,
				target: target.String(),
			})
			key := "ping4|" + iface
			outcomes := pingOutcomes[key]
			err := outcomes[0]
			pingOutcomes[key] = outcomes[1:]
			return time.Millisecond, err
		},
		ping6: func(
			_ context.Context,
			iface string,
			target netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			calls = append(calls, debugProbeTestCall{
				kind:   "ping6",
				iface:  iface,
				target: target.String(),
			})
			key := "ping6|" + iface
			outcomes := pingOutcomes[key]
			err := outcomes[0]
			pingOutcomes[key] = outcomes[1:]
			return time.Millisecond, err
		},
		listAddrs: func(
			_ context.Context,
			_ *slog.Logger,
			iface string,
		) ([]netif.CurrentAddr, error) {
			listCalls = append(listCalls, iface)
			switch iface {
			case "enatt0":
				return []netif.CurrentAddr{
					{CIDR: "127.0.0.1/8", Family: "inet"},
					{CIDR: "192.0.2.10/24", Family: "inet"},
					{CIDR: "fe80::1/64", Family: "inet6"},
					{CIDR: "2001:db8:1::10/64", Family: "inet6"},
				}, nil
			case "enwebpass0":
				return []netif.CurrentAddr{
					{CIDR: "198.51.100.20/24", Family: "inet"},
				}, nil
			case "enmonkeybrains":
				return []netif.CurrentAddr{
					{CIDR: "2001:db8:3::30/64", Family: "inet6"},
				}, nil
			default:
				return nil, errors.New("unexpected interface")
			}
		},
		renderNPT: func(
			_ context.Context,
			_ *slog.Logger,
		) (npt.RenderedTable, error) {
			renderCount++
			return npt.RenderedTable{
				Prerouting: []string{
					`iif "enatt0" ip6 daddr 2001:db8::/64 dnat prefix to fd00::/64`,
				},
				Postrouting: []string{
					`oif "enmonkeybrains" ip6 saddr fd00::/64 snat prefix to 2001:db8::/64`,
				},
			}, nil
		},
		prefixSource: &fakeDebugPrefixSource{
			prefixes: map[string]netip.Prefix{
				"enatt0": netip.MustParsePrefix("2001:db8:100::/56"),
			},
		},
	}

	var output strings.Builder
	err := runDebugProbeViewWithDependencies(
		context.Background(),
		&output,
		debugProbeTestLogger(),
		cfg,
		"connectivity",
		nil,
		dependencies,
	)
	if err != nil {
		t.Fatalf("connectivity returned error: %v", err)
	}

	wantListCalls := []string{"enatt0", "enmonkeybrains", "enwebpass0"}
	if !reflect.DeepEqual(listCalls, wantListCalls) {
		t.Fatalf("ListAddrs calls = %v, want %v", listCalls, wantListCalls)
	}
	if renderCount != 1 {
		t.Fatalf("RenderTable calls = %d, want 1", renderCount)
	}
	prefixSource := dependencies.prefixSource.(*fakeDebugPrefixSource)
	if !reflect.DeepEqual(prefixSource.calls, wantListCalls) {
		t.Fatalf("Prefix calls = %v, want %v", prefixSource.calls, wantListCalls)
	}

	wantCalls := []debugProbeTestCall{
		{kind: "ping4", iface: "enatt0", target: "1.1.1.1"},
		{kind: "ping4", iface: "enatt0", target: "1.1.1.1"},
		{kind: "ping6", iface: "enatt0", target: "2606:4700:4700::1111"},
		{kind: "ping6", iface: "enatt0", target: "2606:4700:4700::1111"},
		{kind: "ping6", iface: "enmonkeybrains", target: "2606:4700:4700::1111"},
		{kind: "ping6", iface: "enmonkeybrains", target: "2606:4700:4700::1111"},
		{kind: "ping4", iface: "enwebpass0", target: "1.1.1.1"},
		{kind: "ping4", iface: "enwebpass0", target: "1.1.1.1"},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("probe calls mismatch\ngot:  %#v\nwant: %#v", calls, wantCalls)
	}

	rendered := output.String()
	assertDebugProbeOrderedText(t, rendered, "att", "monkeybrains", "webpass")
	assertDebugProbeContains(t, rendered,
		"WAN", "IFACE", "IPv4", "IPv6", "P4", "P6", "NPT", "PD",
		"192.0.2.10/24", "2001:db8:1::10/64",
		"198.51.100.20/24", "2001:db8:3::30/64",
		"2001:db8:100::/56", "<none>",
		"att", "enatt0", "OK", "FAIL",
	)
	attLine := debugProbeLineContaining(t, rendered, "enatt0")
	assertDebugProbeContains(t, attLine, "OK", "FAIL", "OK", "2001:db8:100::/56")
	webpassLine := debugProbeLineContaining(t, rendered, "enwebpass0")
	assertDebugProbeContains(t, webpassLine, "FAIL", "SKIP", "FAIL", "<none>")
	monkeybrainsLine := debugProbeLineContaining(t, rendered, "enmonkeybrains")
	assertDebugProbeContains(t, monkeybrainsLine, "SKIP", "OK", "OK", "<none>")
}

func TestDebugConnectivityPropagatesInspectionErrors(t *testing.T) {
	t.Parallel()

	expectedError := errors.New("netlink unavailable")
	dependencies := debugProbeDependencies{
		listAddrs: func(
			_ context.Context,
			_ *slog.Logger,
			_ string,
		) ([]netif.CurrentAddr, error) {
			return nil, expectedError
		},
		renderNPT: func(
			_ context.Context,
			_ *slog.Logger,
		) (npt.RenderedTable, error) {
			return npt.RenderedTable{}, nil
		},
		prefixSource: &fakeDebugPrefixSource{},
	}

	err := runDebugProbeViewWithDependencies(
		context.Background(),
		io.Discard,
		debugProbeTestLogger(),
		debugProbeTestConfig(),
		"connectivity",
		nil,
		dependencies,
	)
	if !errors.Is(err, expectedError) {
		t.Fatalf("connectivity error = %v, want wrapped %v", err, expectedError)
	}
}

func TestDebugPingViewsUseExpectedIfaceTargetsAndAttempts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		view        string
		args        []string
		wantIface   string
		wantKind    string
		wantTargets []string
	}{
		{
			name:        "ping4 defaults to the lowest mark",
			view:        "ping4",
			args:        nil,
			wantIface:   "enatt0",
			wantKind:    "ping4",
			wantTargets: []string{"1.1.1.1", "8.8.8.8"},
		},
		{
			name:        "ping6 uses explicit interface",
			view:        "ping6",
			args:        []string{"test6"},
			wantIface:   "test6",
			wantKind:    "ping6",
			wantTargets: []string{"2606:4700:4700::1111", "2620:fe::9"},
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var calls []debugProbeTestCall
			attemptCount := 0
			probe := func(
				_ context.Context,
				iface string,
				target netip.Addr,
				_ time.Duration,
			) (time.Duration, error) {
				calls = append(calls, debugProbeTestCall{
					kind:   testCase.wantKind,
					iface:  iface,
					target: target.String(),
				})
				attemptCount++
				if attemptCount == 2 {
					return 0, errors.New("unreachable")
				}
				return time.Duration(attemptCount) * time.Millisecond, nil
			}
			dependencies := debugProbeDependencies{}
			if testCase.view == "ping4" {
				dependencies.ping4 = probe
			} else {
				dependencies.ping6 = probe
			}

			var output strings.Builder
			err := runDebugProbeViewWithDependencies(
				context.Background(),
				&output,
				debugProbeTestLogger(),
				debugProbeTestConfig(),
				testCase.view,
				testCase.args,
				dependencies,
			)
			if err != nil {
				t.Fatalf("%s returned error: %v", testCase.view, err)
			}

			wantCalls := make([]debugProbeTestCall, 0, 6)
			for _, target := range testCase.wantTargets {
				for range 3 {
					wantCalls = append(wantCalls, debugProbeTestCall{
						kind:   testCase.wantKind,
						iface:  testCase.wantIface,
						target: target,
					})
				}
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("probe calls mismatch\ngot:  %#v\nwant: %#v", calls, wantCalls)
			}
			rendered := output.String()
			assertDebugProbeOrderedText(
				t,
				rendered,
				testCase.wantTargets[0],
				testCase.wantTargets[1],
			)
			assertDebugProbeContains(
				t,
				rendered,
				"attempt 1:",
				"attempt 2: FAIL: unreachable",
				"3 transmitted, 2 received",
			)
		})
	}
}

func TestDebugCurlViewsForceFamilyAndContinueAfterFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		view      string
		args      []string
		wantIface string
		family    string
	}{
		{
			name:      "curl4 defaults to the lowest mark",
			view:      "curl4",
			args:      nil,
			wantIface: "enatt0",
			family:    "inet",
		},
		{
			name:      "curl6 uses explicit interface",
			view:      "curl6",
			args:      []string{"test6"},
			wantIface: "test6",
			family:    "inet6",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var calls []debugProbeTestCall
			dependencies := debugProbeDependencies{
				httpGet: func(
					_ context.Context,
					iface string,
					family string,
					url string,
					_ time.Duration,
				) (netif.HTTPResult, error) {
					calls = append(calls, debugProbeTestCall{
						kind:   "http",
						iface:  iface,
						family: family,
						target: url,
					})
					if len(calls) == 1 {
						return netif.HTTPResult{
							StatusCode: 503,
							Body:       "192.0.2.44",
						}, nil
					}
					return netif.HTTPResult{}, errors.New("request failed")
				},
			}

			var output strings.Builder
			err := runDebugProbeViewWithDependencies(
				context.Background(),
				&output,
				debugProbeTestLogger(),
				debugProbeTestConfig(),
				testCase.view,
				testCase.args,
				dependencies,
			)
			if err != nil {
				t.Fatalf("%s returned error: %v", testCase.view, err)
			}

			wantCalls := []debugProbeTestCall{
				{
					kind:   "http",
					iface:  testCase.wantIface,
					family: testCase.family,
					target: "https://ifconfig.co",
				},
				{
					kind:   "http",
					iface:  testCase.wantIface,
					family: testCase.family,
					target: "https://api.ipify.org",
				},
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("HTTP calls mismatch\ngot:  %#v\nwant: %#v", calls, wantCalls)
			}
			rendered := output.String()
			assertDebugProbeOrderedText(
				t,
				rendered,
				"https://ifconfig.co",
				"https://api.ipify.org",
			)
			assertDebugProbeContains(t, rendered, "192.0.2.44", "FAIL: request failed")
		})
	}
}

func TestDebugLoadBalanceViewsUseSixSequentialUnboundRequests(t *testing.T) {
	t.Parallel()

	tests := []struct {
		view   string
		family string
	}{
		{view: "lb4", family: "inet"},
		{view: "lb6", family: "inet6"},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.view, func(t *testing.T) {
			t.Parallel()

			var calls []debugProbeTestCall
			dependencies := debugProbeDependencies{
				httpGet: func(
					_ context.Context,
					iface string,
					family string,
					url string,
					_ time.Duration,
				) (netif.HTTPResult, error) {
					calls = append(calls, debugProbeTestCall{
						kind:   "http",
						iface:  iface,
						family: family,
						target: url,
					})
					if len(calls) == 2 {
						return netif.HTTPResult{}, errors.New("timeout")
					}
					return netif.HTTPResult{Body: "198.51.100.8"}, nil
				},
			}

			var output strings.Builder
			err := runDebugProbeViewWithDependencies(
				context.Background(),
				&output,
				debugProbeTestLogger(),
				debugProbeTestConfig(),
				testCase.view,
				nil,
				dependencies,
			)
			if err != nil {
				t.Fatalf("%s returned error: %v", testCase.view, err)
			}

			wantCalls := debugProbeExpectedHTTPCalls(
				testCase.family,
				[]string{"", "", "", "", "", ""},
			)
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("HTTP calls mismatch\ngot:  %#v\nwant: %#v", calls, wantCalls)
			}
			rendered := output.String()
			assertDebugProbeOrderedText(
				t,
				rendered,
				testCase.view+" iter 1",
				testCase.view+" iter 2",
				testCase.view+" iter 3",
				testCase.view+" iter 4",
				testCase.view+" iter 5",
				testCase.view+" iter 6",
			)
			assertDebugProbeContains(t, rendered, "FAIL: timeout", "198.51.100.8")
		})
	}
}

func TestDebugLoadBalanceIfaceViewsRotateConfiguredWANs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		view   string
		family string
	}{
		{view: "lb4-ifaces", family: "inet"},
		{view: "lb6-ifaces", family: "inet6"},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.view, func(t *testing.T) {
			t.Parallel()

			var calls []debugProbeTestCall
			dependencies := debugProbeDependencies{
				httpGet: func(
					_ context.Context,
					iface string,
					family string,
					url string,
					_ time.Duration,
				) (netif.HTTPResult, error) {
					calls = append(calls, debugProbeTestCall{
						kind:   "http",
						iface:  iface,
						family: family,
						target: url,
					})
					return netif.HTTPResult{Body: iface + "-address"}, nil
				},
			}

			var output strings.Builder
			err := runDebugProbeViewWithDependencies(
				context.Background(),
				&output,
				debugProbeTestLogger(),
				debugProbeTestConfig(),
				testCase.view,
				nil,
				dependencies,
			)
			if err != nil {
				t.Fatalf("%s returned error: %v", testCase.view, err)
			}

			ifaces := []string{
				"enatt0",
				"enmonkeybrains",
				"enwebpass0",
				"enatt0",
				"enmonkeybrains",
				"enwebpass0",
			}
			wantCalls := debugProbeExpectedHTTPCalls(testCase.family, ifaces)
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("HTTP calls mismatch\ngot:  %#v\nwant: %#v", calls, wantCalls)
			}
			assertDebugProbeOrderedText(
				t,
				output.String(),
				"iter 1 via enatt0",
				"iter 2 via enmonkeybrains",
				"iter 3 via enwebpass0",
				"iter 4 via enatt0",
				"iter 5 via enmonkeybrains",
				"iter 6 via enwebpass0",
			)
		})
	}
}

func TestDebugLoadBalanceIfaceViewRequiresUsableWAN(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		IfMgr: config.IfMgrSection{
			WAN: map[string]config.IfMgrWANEntry{
				"att":     {},
				"webpass": {},
			},
		},
	}
	err := runDebugProbeViewWithDependencies(
		context.Background(),
		io.Discard,
		debugProbeTestLogger(),
		cfg,
		"lb4-ifaces",
		nil,
		debugProbeDependencies{},
	)
	if err == nil || !strings.Contains(err.Error(), "no usable WAN interfaces") {
		t.Fatalf("lb4-ifaces error = %v, want usable-WAN configuration error", err)
	}
}

func TestDebugProbeViewValidatesArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		view string
		args []string
	}{
		{view: "connectivity", args: []string{"extra"}},
		{view: "ping4", args: []string{"one", "two"}},
		{view: "ping6", args: []string{"one", "two"}},
		{view: "curl4", args: []string{"one", "two"}},
		{view: "curl6", args: []string{"one", "two"}},
		{view: "lb4", args: []string{"extra"}},
		{view: "lb6", args: []string{"extra"}},
		{view: "lb4-ifaces", args: []string{"extra"}},
		{view: "lb6-ifaces", args: []string{"extra"}},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.view, func(t *testing.T) {
			t.Parallel()

			err := runDebugProbeViewWithDependencies(
				context.Background(),
				io.Discard,
				debugProbeTestLogger(),
				debugProbeTestConfig(),
				testCase.view,
				testCase.args,
				debugProbeDependencies{},
			)
			if err == nil || !strings.Contains(err.Error(), "positional") {
				t.Fatalf("%s error = %v, want positional argument error", testCase.view, err)
			}
		})
	}
}

// TestDebugProbeDefaultIfaceIsTheLowestMark pins that the default interface
// comes from the configured mark order rather than a provider name in code, so
// a gateway with no provider called att still has a default.
func TestDebugProbeDefaultIfaceIsTheLowestMark(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		IfMgr: config.IfMgrSection{
			WAN: map[string]config.IfMgrWANEntry{
				"monkeybrains": {Iface: "enmonkeybrains", FwMark: 3},
				"webpass":      {Iface: "enwebpass0", FwMark: 2},
			},
		},
	}
	var calls []debugProbeTestCall
	dependencies := debugProbeDependencies{
		ping4: func(
			_ context.Context,
			iface string,
			target netip.Addr,
			_ time.Duration,
		) (time.Duration, error) {
			calls = append(calls, debugProbeTestCall{kind: "ping4", iface: iface, target: target.String()})
			return time.Millisecond, nil
		},
	}
	err := runDebugProbeViewWithDependencies(
		context.Background(),
		io.Discard,
		debugProbeTestLogger(),
		cfg,
		"ping4",
		nil,
		dependencies,
	)
	if err != nil {
		t.Fatalf("ping4 returned error: %v", err)
	}
	if len(calls) == 0 {
		t.Fatal("ping4 made no probe")
	}
	for _, call := range calls {
		if call.iface != "enwebpass0" {
			t.Fatalf("probe used %q, want the lowest-mark interface enwebpass0", call.iface)
		}
	}
}

// TestDebugProbeDefaultIfaceRequiresAConfiguredProvider pins the error when the
// configuration carries no provider with a usable interface at all.
func TestDebugProbeDefaultIfaceRequiresAConfiguredProvider(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		IfMgr: config.IfMgrSection{
			WAN: map[string]config.IfMgrWANEntry{"webpass": {Iface: ""}},
		},
	}
	err := runDebugProbeViewWithDependencies(
		context.Background(),
		io.Discard,
		debugProbeTestLogger(),
		cfg,
		"ping4",
		nil,
		debugProbeDependencies{},
	)
	if err == nil || !strings.Contains(err.Error(), "no configured WAN has a usable interface") {
		t.Fatalf("ping4 error = %v, want the no-usable-WAN error", err)
	}
}

func debugProbeTestConfig() *config.Config {
	return &config.Config{
		IfMgr: config.IfMgrSection{
			WAN: map[string]config.IfMgrWANEntry{
				"monkeybrains": {
					Iface: "enmonkeybrains", TableID: 300, FwMark: 3, FwMarkPrio: 300,
					FromPrio: 57, NptPrefix: "", V4Source: "", Tier: 1, Weight: 1,
				},
				"webpass": {
					Iface: "enwebpass0", TableID: 200, FwMark: 2, FwMarkPrio: 200,
					FromPrio: 56, NptPrefix: "", V4Source: "", Tier: 0, Weight: 1,
				},
				"att": {
					Iface: "enatt0", TableID: 100, FwMark: 1, FwMarkPrio: 100,
					FromPrio: 55, NptPrefix: "", V4Source: "", Tier: 0, Weight: 1,
				},
				// A provider with no interface stays in the fixture so the
				// usable-interface filter is still exercised. Its mark is not
				// the lowest, so it can never become the default either.
				"noiface": {
					Iface: "", TableID: 600, FwMark: 4, FwMarkPrio: 600,
					FromPrio: 58, NptPrefix: "", V4Source: "", Tier: 2, Weight: 1,
				},
			},
		},
	}
}

func debugProbeTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func debugProbeExpectedHTTPCalls(
	family string,
	ifaces []string,
) []debugProbeTestCall {
	targets := []string{"https://ifconfig.co", "https://api.ipify.org"}
	calls := make([]debugProbeTestCall, 0, len(ifaces))
	for index, iface := range ifaces {
		calls = append(calls, debugProbeTestCall{
			kind:   "http",
			iface:  iface,
			family: family,
			target: targets[index%len(targets)],
		})
	}
	return calls
}

func assertDebugProbeContains(t *testing.T, text string, fragments ...string) {
	t.Helper()

	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Errorf("output %q does not contain %q", text, fragment)
		}
	}
}

func assertDebugProbeOrderedText(t *testing.T, text string, fragments ...string) {
	t.Helper()

	previousIndex := -1
	for _, fragment := range fragments {
		index := strings.Index(text, fragment)
		if index < 0 {
			t.Fatalf("output %q does not contain %q", text, fragment)
		}
		if index <= previousIndex {
			t.Fatalf("output %q does not order %q after prior fragment", text, fragment)
		}
		previousIndex = index
	}
}

func debugProbeLineContaining(t *testing.T, text string, fragment string) string {
	t.Helper()

	for line := range strings.SplitSeq(text, "\n") {
		if strings.Contains(line, fragment) {
			return line
		}
	}
	t.Fatalf("output %q has no line containing %q", text, fragment)
	return ""
}
