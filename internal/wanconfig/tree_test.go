package wanconfig

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

// testMember returns a member carrying every value the model requires of a
// provider, so a test that mutates one field fails on that field alone.
func testMember(name string, iface string) Member {
	return Member{
		Name:       name,
		Iface:      iface,
		Tier:       0,
		Weight:     1,
		TableID:    100,
		FwMark:     1,
		FwMarkPrio: 100,
		FromPrio:   55,
	}
}

// TestConfigItems_DescribesEveryMemberAndTranslation pins the published shape
// for a gateway like the testbed's: three members, one fallback, a probed
// member with a forced DSCP value, an unprobed one, a member with a disabled
// probe, a source pin, and two static mappings, two carrying a translation
// pair, and a group holding every value. Every path here is one a RESTCONF reader sees, so a change to
// this list is a change to the served tree.
func TestConfigItems_DescribesEveryMemberAndTranslation(t *testing.T) {
	t.Parallel()
	internal := netip.MustParsePrefix("3d06:bad:b01:210::/60")
	att := testMember("att", "enatt0.3242")
	att.ProbePolicy = "att"
	att.NPTInternal = internal
	att.NPTExternal = netip.MustParsePrefix("2001:db8:a::/60")
	att.ForcedDSCP = 8
	att.Health = &ProbeSettings{
		Enabled:              true,
		PingCount:            new(uint8(3)),
		SuccessThreshold:     new(uint8(2)),
		FailureThreshold:     new(uint8(2)),
		RecoveryThreshold:    new(uint8(2)),
		CheckIntervalSeconds: new(uint32(10)),
		TargetsV4:            []netip.Addr{netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.11")},
		TargetsV6:            []netip.Addr{netip.MustParseAddr("2001:db8:53::1")},
		HTTPURLs:             []string{"https://example.test/ip"},
	}
	monkeybrains := testMember("monkeybrains", "enmbrains0")
	monkeybrains.Tier = 1
	monkeybrains.TableID = 300
	monkeybrains.FwMark = 3
	monkeybrains.FwMarkPrio = 300
	monkeybrains.FromPrio = 57
	webpass := testMember("webpass", "enwebpass0")
	webpass.Weight = 2
	webpass.NPTInternal = internal
	webpass.NPTExternal = netip.MustParsePrefix("2001:db8:b::/60")
	webpass.TableID = 200
	webpass.FwMark = 2
	webpass.FwMarkPrio = 200
	webpass.FromPrio = 56
	webpass.V4Source = "192.0.2.2"
	webpass.StaticMappings = []StaticMapping{
		{External: netip.MustParseAddr("198.51.100.2"), Internal: netip.MustParseAddr("10.250.250.2")},
		{External: netip.MustParseAddr("198.51.100.3"), Internal: netip.MustParseAddr("10.250.250.3")},
	}
	webpass.Health = &ProbeSettings{Enabled: false}
	gateway := Gateway{
		InternalIface: "eninternal0",
		HashMode:      "random",
		Group: GroupSettings{
			ReservedTables:     []uint32{400, 500},
			InternalPrefix:     internal,
			OpnsenseEdgeV6:     netip.MustParseAddr("2001:db8:fe::2"),
			MwanbrEdgeV6:       netip.MustParseAddr("2001:db8:fe::3"),
			InternalNetV4:      netip.MustParsePrefix("192.0.2.0/29"),
			ProbeTimeoutMillis: 2000,
		},
		Members: []Member{att, monkeybrains, webpass},
	}

	items, err := ConfigItems(gateway)
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}

	const (
		internalLink = "/ietf-interfaces:interfaces/interface[name='eninternal0']"
		attLink      = "/ietf-interfaces:interfaces/interface[name='enatt0.3242']"
		mbLink       = "/ietf-interfaces:interfaces/interface[name='enmbrains0']"
		webpassLink  = "/ietf-interfaces:interfaces/interface[name='enwebpass0']"
		group        = "/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group"
	)
	want := []Item{
		{Path: internalLink + "/type", Value: "iana-if-type:other"},
		{Path: internalLink + "/enabled", Value: "true"},
		{Path: internalLink + "/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: internalLink + "/ietf-ip:ipv6/enabled", Value: "true"},

		{Path: attLink + "/type", Value: "iana-if-type:other"},
		{Path: attLink + "/enabled", Value: "true"},
		{Path: attLink + "/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: attLink + "/ietf-ip:ipv6/enabled", Value: "true"},
		{Path: attLink + "/goodkind-mwan-steering:steering/tier", Value: "0"},
		{Path: attLink + "/goodkind-mwan-steering:steering/weight", Value: "1"},
		{Path: attLink + "/goodkind-mwan-steering:steering/probe-policy", Value: "att"},
		{Path: attLink + "/goodkind-mwan-steering:wan/name", Value: "att"},
		{Path: attLink + "/goodkind-mwan-steering:wan/table-id", Value: "100"},
		{Path: attLink + "/goodkind-mwan-steering:wan/fw-mark", Value: "1"},
		{Path: attLink + "/goodkind-mwan-steering:wan/fw-mark-prio", Value: "100"},
		{Path: attLink + "/goodkind-mwan-steering:wan/from-prio", Value: "55"},
		{Path: attLink + "/goodkind-mwan-steering:wan/npt-prefix", Value: "2001:db8:a::/60"},
		{Path: attLink + "/goodkind-mwan-steering:wan/forced-dscp", Value: "8"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/enabled", Value: "true"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/ping-count", Value: "3"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/success-threshold", Value: "2"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/failure-threshold", Value: "2"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/recovery-threshold", Value: "2"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/check-interval", Value: "10"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/targets-v4", Value: "192.0.2.10"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/targets-v4", Value: "192.0.2.11"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/targets-v6", Value: "2001:db8:53::1"},
		{Path: attLink + "/goodkind-mwan-steering:wan/health/http-urls", Value: "https://example.test/ip"},

		{Path: mbLink + "/type", Value: "iana-if-type:other"},
		{Path: mbLink + "/enabled", Value: "true"},
		{Path: mbLink + "/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: mbLink + "/ietf-ip:ipv6/enabled", Value: "true"},
		{Path: mbLink + "/goodkind-mwan-steering:steering/tier", Value: "1"},
		{Path: mbLink + "/goodkind-mwan-steering:steering/weight", Value: "1"},
		{Path: mbLink + "/goodkind-mwan-steering:wan/name", Value: "monkeybrains"},
		{Path: mbLink + "/goodkind-mwan-steering:wan/table-id", Value: "300"},
		{Path: mbLink + "/goodkind-mwan-steering:wan/fw-mark", Value: "3"},
		{Path: mbLink + "/goodkind-mwan-steering:wan/fw-mark-prio", Value: "300"},
		{Path: mbLink + "/goodkind-mwan-steering:wan/from-prio", Value: "57"},

		{Path: webpassLink + "/type", Value: "iana-if-type:other"},
		{Path: webpassLink + "/enabled", Value: "true"},
		{Path: webpassLink + "/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: webpassLink + "/ietf-ip:ipv6/enabled", Value: "true"},
		{Path: webpassLink + "/goodkind-mwan-steering:steering/tier", Value: "0"},
		{Path: webpassLink + "/goodkind-mwan-steering:steering/weight", Value: "2"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/name", Value: "webpass"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/table-id", Value: "200"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/fw-mark", Value: "2"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/fw-mark-prio", Value: "200"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/from-prio", Value: "56"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/npt-prefix", Value: "2001:db8:b::/60"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/v4-source", Value: "192.0.2.2"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/static-mapping[external='198.51.100.2']/internal", Value: "10.250.250.2"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/static-mapping[external='198.51.100.3']/internal", Value: "10.250.250.3"},
		{Path: webpassLink + "/goodkind-mwan-steering:wan/health/enabled", Value: "false"},

		{Path: group + "/hash-mode", Value: "random"},
		{Path: group + "/reserved-tables", Value: "400"},
		{Path: group + "/reserved-tables", Value: "500"},
		{Path: group + "/translation/internal-prefix", Value: "3d06:bad:b01:210::/60"},
		{Path: group + "/translation/opnsense-edge-v6", Value: "2001:db8:fe::2"},
		{Path: group + "/translation/mwanbr-edge-v6", Value: "2001:db8:fe::3"},
		{Path: group + "/routes/internal-iface", Value: "eninternal0"},
		{Path: group + "/routes/internal-net-v4", Value: "192.0.2.0/29"},
		{Path: group + "/health/probe-timeout", Value: "2000"},

		{Path: "/ietf-nat:nat/instances/instance[id='1']/name", Value: "att"},
		{Path: "/ietf-nat:nat/instances/instance[id='1']/type", Value: "ietf-nat:nptv6"},
		{Path: "/ietf-nat:nat/instances/instance[id='1']/enable", Value: "true"},
		{Path: "/ietf-nat:nat/instances/instance[id='1']/policy[id='1']/nptv6-prefixes[internal-ipv6-prefix='3d06:bad:b01:210::/60']/external-ipv6-prefix", Value: "2001:db8:a::/60"},
		{Path: "/ietf-nat:nat/instances/instance[id='2']/name", Value: "webpass"},
		{Path: "/ietf-nat:nat/instances/instance[id='2']/type", Value: "ietf-nat:nptv6"},
		{Path: "/ietf-nat:nat/instances/instance[id='2']/enable", Value: "true"},
		{Path: "/ietf-nat:nat/instances/instance[id='2']/policy[id='1']/nptv6-prefixes[internal-ipv6-prefix='3d06:bad:b01:210::/60']/external-ipv6-prefix", Value: "2001:db8:b::/60"},
	}
	if !slices.Equal(items, want) {
		t.Fatalf("items differ\n got: %v\nwant: %v", items, want)
	}
}

// TestConfigItems_PublishesExactlyTheSettingsADisabledProbeHolds pins the
// disabled-probe contract: the flag publishes as false, every setting the
// loaded probe holds publishes, a zero setting publishes as zero, and a setting
// the file left out publishes nothing.
func TestConfigItems_PublishesExactlyTheSettingsADisabledProbeHolds(t *testing.T) {
	t.Parallel()
	member := testMember("att", "enatt0")
	member.Health = &ProbeSettings{
		Enabled:              false,
		PingCount:            new(uint8(4)),
		CheckIntervalSeconds: new(uint32(0)),
		TargetsV4:            []netip.Addr{netip.MustParseAddr("192.0.2.20")},
		HTTPURLs:             []string{"https://example.test/att"},
	}
	items, err := ConfigItems(Gateway{InternalIface: "eninternal0", Members: []Member{member}})
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	const health = "/ietf-interfaces:interfaces/interface[name='enatt0']/goodkind-mwan-steering:wan/health"
	var got []Item
	for _, item := range items {
		if strings.HasPrefix(item.Path, health+"/") {
			got = append(got, item)
		}
	}
	want := []Item{
		{Path: health + "/enabled", Value: "false"},
		{Path: health + "/ping-count", Value: "4"},
		{Path: health + "/check-interval", Value: "0"},
		{Path: health + "/targets-v4", Value: "192.0.2.20"},
		{Path: health + "/http-urls", Value: "https://example.test/att"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("health items differ\n got: %v\nwant: %v", got, want)
	}
}

// TestConfigItems_PublishesOnlyWhatAnUnconfiguredGatewayHolds pins the absent
// cases: a member with no probe gets no probe-policy and no health container, a
// provider with no prefix, source pin, or forced DSCP value publishes none of
// them, and a gateway holding no group values publishes only the internal link
// under the steering group.
func TestConfigItems_PublishesOnlyWhatAnUnconfiguredGatewayHolds(t *testing.T) {
	t.Parallel()
	items, err := ConfigItems(Gateway{
		InternalIface: "eninternal0",
		Members:       []Member{testMember("att", "enatt0")},
	})
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	const attLink = "/ietf-interfaces:interfaces/interface[name='enatt0']"
	want := []Item{
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/type", Value: "iana-if-type:other"},
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/enabled", Value: "true"},
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/ietf-ip:ipv6/enabled", Value: "true"},
		{Path: attLink + "/type", Value: "iana-if-type:other"},
		{Path: attLink + "/enabled", Value: "true"},
		{Path: attLink + "/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: attLink + "/ietf-ip:ipv6/enabled", Value: "true"},
		{Path: attLink + "/goodkind-mwan-steering:steering/tier", Value: "0"},
		{Path: attLink + "/goodkind-mwan-steering:steering/weight", Value: "1"},
		{Path: attLink + "/goodkind-mwan-steering:wan/name", Value: "att"},
		{Path: attLink + "/goodkind-mwan-steering:wan/table-id", Value: "100"},
		{Path: attLink + "/goodkind-mwan-steering:wan/fw-mark", Value: "1"},
		{Path: attLink + "/goodkind-mwan-steering:wan/fw-mark-prio", Value: "100"},
		{Path: attLink + "/goodkind-mwan-steering:wan/from-prio", Value: "55"},
		{Path: "/ietf-interfaces:interfaces/goodkind-mwan-steering:steering-group/routes/internal-iface", Value: "eninternal0"},
	}
	if !slices.Equal(items, want) {
		t.Fatalf("items differ\n got: %v\nwant: %v", items, want)
	}
}

// TestConfigItems_PublishesTheDaemonSettingsItHolds pins the daemon
// container: every present section publishes its leaves under
// /goodkind-mwan-steering:daemon, leaf-list entries are addressed by
// value, and an absent section publishes nothing.
func TestConfigItems_PublishesTheDaemonSettingsItHolds(t *testing.T) {
	t.Parallel()
	gateway := Gateway{
		InternalIface: "eninternal0",
		Members:       []Member{testMember("att", "enatt0")},
		Daemon: DaemonSettings{
			Watchdog: WatchdogSettings{
				Present:                      true,
				DeployWindowMinutes:          30,
				ConnectivityTimeoutSeconds:   60,
				CheckIntervalHealthySeconds:  30,
				CheckIntervalDegradedSeconds: 10,
				PostRollbackGraceSeconds:     120,
				AlertCooldownSeconds:         300,
				DeployGracePeriodSeconds:     60,
				MaxRollbackAttempts:          3,
				SnapshotHealthyThreshold:     20,
				MaxKnownGoodSnapshots:        3,
				PingTargets: []netip.Addr{
					netip.MustParseAddr("2606:4700:4700::1111"),
					netip.MustParseAddr("1.1.1.1"),
					// A zero address must publish nothing, never a value
					// the typed leaf rejects.
					{},
				},
			},
			OOB: OOBSettings{
				V6Present: true, V6Iface: "enoob0",
				V6Addr:    netip.MustParseAddr("2001:db8:ff::2"),
				V6TableID: 500, ManageSLAACRule: true, SLAACRulePriority: 7,
			},
			Tap: TapSettings{
				Present: true, Unit: "cloudflared-oob.service",
				DowngradePatterns: []string{"receive buffer size"},
			},
		},
	}

	items, err := ConfigItems(gateway)
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	served := map[string][]string{}
	for _, item := range items {
		served[item.Path] = append(served[item.Path], item.Value)
	}
	want := map[string]string{
		"/goodkind-mwan-steering:daemon/watchdog/deploy-window-minutes":      "30",
		"/goodkind-mwan-steering:daemon/watchdog/max-rollback-attempts":      "3",
		"/goodkind-mwan-steering:daemon/watchdog/snapshot-healthy-threshold": "20",
		"/goodkind-mwan-steering:daemon/oob/ipv6/interface":                  "enoob0",
		"/goodkind-mwan-steering:daemon/oob/ipv6/address":                    "2001:db8:ff::2",
		"/goodkind-mwan-steering:daemon/oob/ipv6/table-id":                   "500",
		"/goodkind-mwan-steering:daemon/oob/ipv6/manage-slaac-rule":          "true",
		"/goodkind-mwan-steering:daemon/tap/unit":                            "cloudflared-oob.service",
		"/goodkind-mwan-steering:daemon/tap/downgrade-pattern":               "receive buffer size",
	}
	for path, value := range want {
		got := served[path]
		if len(got) != 1 || got[0] != value {
			t.Fatalf("path %s = %v, want [%s]", path, got, value)
		}
	}
	pings := served["/goodkind-mwan-steering:daemon/watchdog/probe-targets/ping"]
	if len(pings) != 2 || pings[0] != "2606:4700:4700::1111" || pings[1] != "1.1.1.1" {
		t.Fatalf("ping targets = %v, want both families in order", pings)
	}
	for path := range served {
		if strings.HasPrefix(path, "/goodkind-mwan-steering:daemon/oob/ipv4") {
			t.Fatalf("oob ipv4 published without a config carrying it: %s", path)
		}
	}
}

// TestConfigItems_PublishesNoDaemonSettingsWhenAbsent pins the presence
// gate: a gateway whose configuration carries none of the daemon
// sections publishes nothing under the daemon container.
func TestConfigItems_PublishesNoDaemonSettingsWhenAbsent(t *testing.T) {
	t.Parallel()
	items, err := ConfigItems(Gateway{
		InternalIface: "eninternal0",
		Members:       []Member{testMember("att", "enatt0")},
	})
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	for _, item := range items {
		if strings.HasPrefix(item.Path, "/goodkind-mwan-steering:daemon") {
			t.Fatalf("daemon settings published with none configured: %v", item)
		}
	}
}

// TestConfigItems_RejectsWhatAPathCannotCarry pins the failure contract: an
// unpublishable gateway returns ErrInvalidGateway and no items, so the caller
// logs and keeps running rather than writing a partial tree. Each case breaks
// exactly one value of an otherwise valid gateway, so it fails on that value.
func TestConfigItems_RejectsWhatAPathCannotCarry(t *testing.T) {
	t.Parallel()
	internal := netip.MustParsePrefix("3d06:bad:b01:210::/60")
	withMember := func(mutate func(member *Member)) Gateway {
		member := testMember("att", "enatt0")
		mutate(&member)
		return Gateway{InternalIface: "eninternal0", Members: []Member{member}}
	}
	withGroup := func(mutate func(group *GroupSettings)) Gateway {
		gateway := withMember(func(*Member) {})
		mutate(&gateway.Group)
		return gateway
	}
	cases := map[string]Gateway{
		"empty internal link": {InternalIface: "", Members: nil},
		"empty member name":   withMember(func(member *Member) { member.Name = "" }),
		"quote in link":       withMember(func(member *Member) { member.Iface = "en'att0" }),
		"duplicate link": {InternalIface: "eninternal0", Members: []Member{
			testMember("att", "enatt0"), testMember("webpass", "enatt0"),
		}},
		"member on the internal link": withMember(func(member *Member) { member.Iface = "eninternal0" }),
		"one translation prefix":      withMember(func(member *Member) { member.NPTInternal = internal }),
		"ipv4 translation prefix": withMember(func(member *Member) {
			member.NPTInternal = internal
			member.NPTExternal = netip.MustParsePrefix("10.0.0.0/8")
		}),
		"zero weight": withMember(func(member *Member) { member.Weight = 0 }),
		"unknown hash mode": {
			InternalIface: "eninternal0", HashMode: "round-robin",
			Members: []Member{testMember("att", "enatt0")},
		},
		"zero table id":           withMember(func(member *Member) { member.TableID = 0 }),
		"zero firewall mark":      withMember(func(member *Member) { member.FwMark = 0 }),
		"forced dscp above range": withMember(func(member *Member) { member.ForcedDSCP = 64 }),
		"ipv6 source pin":         withMember(func(member *Member) { member.V4Source = "2001:db8::1" }),
		"ipv6 mapped external": withMember(func(member *Member) {
			member.StaticMappings = []StaticMapping{
				{External: netip.MustParseAddr("2001:db8::2"), Internal: netip.MustParseAddr("10.250.250.2")},
			}
		}),
		"missing mapped internal": withMember(func(member *Member) {
			member.StaticMappings = []StaticMapping{{External: netip.MustParseAddr("198.51.100.2")}}
		}),
		"external mapped twice": withMember(func(member *Member) {
			member.StaticMappings = []StaticMapping{
				{External: netip.MustParseAddr("198.51.100.2"), Internal: netip.MustParseAddr("10.250.250.2")},
				{External: netip.MustParseAddr("198.51.100.2"), Internal: netip.MustParseAddr("10.250.250.3")},
			}
		}),
		"unparsable source pin": withMember(func(member *Member) { member.V4Source = "not-an-address" }),
		"ipv6 target in the ipv4 list": withMember(func(member *Member) {
			member.Health = &ProbeSettings{Enabled: true, TargetsV4: []netip.Addr{netip.MustParseAddr("2001:db8::1")}}
		}),
		"ipv4 target in the ipv6 list": withMember(func(member *Member) {
			member.Health = &ProbeSettings{Enabled: true, TargetsV6: []netip.Addr{netip.MustParseAddr("192.0.2.1")}}
		}),
		"ipv4 internal prefix": withGroup(func(group *GroupSettings) {
			group.InternalPrefix = netip.MustParsePrefix("10.0.0.0/8")
		}),
		"ipv4 router edge address": withGroup(func(group *GroupSettings) {
			group.OpnsenseEdgeV6 = netip.MustParseAddr("192.0.2.1")
		}),
		"ipv4 gateway edge address": withGroup(func(group *GroupSettings) {
			group.MwanbrEdgeV6 = netip.MustParseAddr("192.0.2.1")
		}),
		"ipv6 internal network": withGroup(func(group *GroupSettings) {
			group.InternalNetV4 = netip.MustParsePrefix("2001:db8::/64")
		}),
	}
	if _, err := ConfigItems(withMember(func(*Member) {})); err != nil {
		t.Fatalf("the unbroken gateway every case starts from is rejected: %v", err)
	}
	for name, gateway := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			items, err := ConfigItems(gateway)
			if !errors.Is(err, ErrInvalidGateway) {
				t.Fatalf("err = %v, want ErrInvalidGateway", err)
			}
			if items != nil {
				t.Fatalf("items = %v, want none", items)
			}
		})
	}
}
