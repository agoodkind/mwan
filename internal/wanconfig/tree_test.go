package wanconfig

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/networkd"
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

// renderedLinkSpec is a link like the free-form instance document's: matched
// by driver, a static IPv4 address with a gateway and an extra source
// address, a DHCPv6 client with every delegation leaf, and one free-form line.
func renderedLinkSpec(name string) *networkd.Spec {
	return &networkd.Spec{
		Name:            name,
		TableID:         200,
		Match:           networkd.Match{Driver: "igc"},
		HardwareAddress: "02:00:5e:00:53:01",
		IPv4: &networkd.FamilyV4{
			Family: networkd.Family{
				Forwarding:  new(true),
				Addresses:   []networkd.Address{{IP: netip.MustParseAddr("203.0.113.2"), PrefixLength: 29}},
				DHCP:        new(false),
				Gateway:     netip.MustParseAddr("203.0.113.1"),
				RouteMetric: new(10),
			},
			SourceAddresses: []netip.Addr{netip.MustParseAddr("203.0.113.3")},
		},
		IPv6: &networkd.FamilyV6{
			Family:   networkd.Family{Forwarding: new(true), DHCP: new(true), RouteMetric: new(10)},
			AcceptRA: new(true),
			Delegation: &networkd.Delegation{
				Hint:                  netip.MustParsePrefix("::/56"),
				DUIDType:              "link-layer-time",
				DUID:                  "00:01:2a:5b:3c:4d:02:00:5e:00:53:01",
				WithoutRA:             "solicit",
				UseDelegatedPrefix:    new(false),
				RouterLifetimeSeconds: new(1800),
			},
		},
		Files: []networkd.File{{
			Kind: networkd.FileNetwork,
			Sections: []networkd.Section{{
				Index:   0,
				Name:    "DHCPv6",
				Entries: []networkd.Entry{{Index: 0, Key: "UseDNS", Value: "no"}},
			}},
		}},
	}
}

// TestConfigItems_DescribesTheLinkTheDaemonRenders pins the published shape
// of a rendered link: the link-files leaf, the link identity, both family
// containers with the steering module's leaves under its namespace, the
// delegation, and the free-form section addressed by its positional keys. A
// member whose files are hand-authored publishes the leaf alone, and a member
// stating nothing publishes nothing.
func TestConfigItems_DescribesTheLinkTheDaemonRenders(t *testing.T) {
	t.Parallel()
	webpass := testMember("webpass", "enwebpass0")
	webpass.TableID = 200
	webpass.FwMark = 2
	webpass.LinkFiles = "rendered"
	webpass.Link = renderedLinkSpec("enwebpass0")
	att := testMember("att", "enatt0")
	att.LinkFiles = "hand-authored"
	monkeybrains := testMember("monkeybrains", "enmbrains0")
	monkeybrains.TableID = 300
	monkeybrains.FwMark = 3
	monkeybrains.FwMarkPrio = 300
	monkeybrains.FromPrio = 57

	items, err := ConfigItems(Gateway{
		InternalIface: "eninternal0",
		Members:       []Member{webpass, att, monkeybrains},
	})
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}

	const (
		webpassLink = "/ietf-interfaces:interfaces/interface[name='enwebpass0']"
		link        = webpassLink + "/goodkind-mwan-steering:link"
		ipv4        = webpassLink + "/ietf-ip:ipv4"
		ipv6        = webpassLink + "/ietf-ip:ipv6"
		delegation  = ipv6 + "/goodkind-mwan-steering:delegation"
		section     = webpassLink + "/goodkind-mwan-steering:networkd/file[kind='network']/section[index='0']"
	)
	var got []Item
	for _, item := range items {
		if strings.Contains(item.Path, "link") ||
			strings.Contains(item.Path, "ietf-ip:ipv") ||
			strings.Contains(item.Path, "networkd") {
			got = append(got, item)
		}
	}
	want := []Item{
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/ietf-ip:ipv6/enabled", Value: "true"},

		{Path: ipv4 + "/enabled", Value: "true"},
		{Path: ipv6 + "/enabled", Value: "true"},
		{Path: webpassLink + "/goodkind-mwan-steering:link-files", Value: "rendered"},
		{Path: link + "/match/driver", Value: "igc"},
		{Path: link + "/hardware-address", Value: "02:00:5e:00:53:01"},
		{Path: ipv4 + "/forwarding", Value: "true"},
		{Path: ipv4 + "/address[ip='203.0.113.2']/prefix-length", Value: "29"},
		{Path: ipv4 + "/goodkind-mwan-steering:dhcp", Value: "false"},
		{Path: ipv4 + "/goodkind-mwan-steering:gateway", Value: "203.0.113.1"},
		{Path: ipv4 + "/goodkind-mwan-steering:route-metric", Value: "10"},
		{Path: ipv4 + "/goodkind-mwan-steering:source-addresses", Value: "203.0.113.3"},
		{Path: ipv6 + "/forwarding", Value: "true"},
		{Path: ipv6 + "/goodkind-mwan-steering:dhcp", Value: "true"},
		{Path: ipv6 + "/goodkind-mwan-steering:route-metric", Value: "10"},
		{Path: ipv6 + "/goodkind-mwan-steering:accept-ra", Value: "true"},
		{Path: delegation + "/hint", Value: "::/56"},
		{Path: delegation + "/duid-type", Value: "link-layer-time"},
		{Path: delegation + "/duid", Value: "00:01:2a:5b:3c:4d:02:00:5e:00:53:01"},
		{Path: delegation + "/without-ra", Value: "solicit"},
		{Path: delegation + "/use-delegated-prefix", Value: "false"},
		{Path: delegation + "/router-lifetime-seconds", Value: "1800"},
		{Path: section + "/name", Value: "DHCPv6"},
		{Path: section + "/entry[index='0']/key", Value: "UseDNS"},
		{Path: section + "/entry[index='0']/value", Value: "no"},

		{Path: "/ietf-interfaces:interfaces/interface[name='enatt0']/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: "/ietf-interfaces:interfaces/interface[name='enatt0']/ietf-ip:ipv6/enabled", Value: "true"},
		{Path: "/ietf-interfaces:interfaces/interface[name='enatt0']/goodkind-mwan-steering:link-files", Value: "hand-authored"},

		{Path: "/ietf-interfaces:interfaces/interface[name='enmbrains0']/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: "/ietf-interfaces:interfaces/interface[name='enmbrains0']/ietf-ip:ipv6/enabled", Value: "true"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("link items differ\n got: %v\nwant: %v", got, want)
	}
}

// TestConfigItems_PublishesAVLANLink pins the VLAN container, whose two
// leaves are both mandatory once the presence container exists.
func TestConfigItems_PublishesAVLANLink(t *testing.T) {
	t.Parallel()
	member := testMember("sonic", "ensonic0.101")
	member.LinkFiles = "rendered"
	member.Link = &networkd.Spec{
		Name: "ensonic0.101",
		VLAN: &networkd.VLAN{Parent: "ensonic0", ID: 101},
	}
	items, err := ConfigItems(Gateway{InternalIface: "eninternal0", Members: []Member{member}})
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	const link = "/ietf-interfaces:interfaces/interface[name='ensonic0.101']/goodkind-mwan-steering:link"
	var got []Item
	for _, item := range items {
		if strings.HasPrefix(item.Path, link+"/") {
			got = append(got, item)
		}
	}
	want := []Item{
		{Path: link + "/vlan/parent", Value: "ensonic0"},
		{Path: link + "/vlan/id", Value: "101"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("vlan items differ\n got: %v\nwant: %v", got, want)
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
		"unknown link-files": withMember(func(member *Member) { member.LinkFiles = "templated" }),
		"link on a hand-authored member": withMember(func(member *Member) {
			member.LinkFiles = "hand-authored"
			member.Link = renderedLinkSpec("enatt0")
		}),
		"link naming another interface": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = renderedLinkSpec("enwebpass0")
		}),
		"vlan id above range": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = &networkd.Spec{Name: "enatt0", VLAN: &networkd.VLAN{Parent: "enphys0", ID: 4095}}
		}),
		"quote in vlan parent": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = &networkd.Spec{Name: "enatt0", VLAN: &networkd.VLAN{Parent: "en'phys0", ID: 1}}
		}),
		"ipv6 address in the ipv4 family": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = renderedLinkSpec("enatt0")
			member.Link.IPv4.Addresses[0].IP = netip.MustParseAddr("2001:db8::2")
		}),
		"ipv4 gateway in the ipv6 family": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = renderedLinkSpec("enatt0")
			member.Link.IPv6.Gateway = netip.MustParseAddr("203.0.113.1")
		}),
		"ipv4 delegation hint": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = renderedLinkSpec("enatt0")
			member.Link.IPv6.Delegation.Hint = netip.MustParsePrefix("10.0.0.0/8")
		}),
		"free-form section with no name": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = renderedLinkSpec("enatt0")
			member.Link.Files[0].Sections[0].Name = ""
		}),
		"free-form line with no key": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = renderedLinkSpec("enatt0")
			member.Link.Files[0].Sections[0].Entries[0].Key = ""
		}),
		"free-form file of an unknown kind": withMember(func(member *Member) {
			member.LinkFiles = "rendered"
			member.Link = renderedLinkSpec("enatt0")
			member.Link.Files[0].Kind = "unit"
		}),
	}
	// A rendered link with every value in range is accepted, so each case
	// above fails on the one value it breaks.
	whole := withMember(func(member *Member) {
		member.LinkFiles = "rendered"
		member.Link = renderedLinkSpec("enatt0")
	})
	if _, err := ConfigItems(whole); err != nil {
		t.Fatalf("the rendered link the link cases start from is rejected: %v", err)
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
