package wanconfig

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

// testMember returns a member carrying every value the model requires of a
// provider, so a test that mutates one field fails on that field alone.
func testMember(name string, iface string) Member {
	return Member{
		Name:            name,
		TranslationIDV4: TranslationInstanceID(name, "ipv4"),
		TranslationIDV6: TranslationInstanceID(name, "ipv6"),
		Iface:           iface,
		Tier:            0,
		Weight:          1,
		TableID:         100,
		FwMark:          1,
		FwMarkPrio:      100,
		FromPrio:        55,
	}
}

func testConnection(name string) interfaceintent.Connection {
	return interfaceintent.Connection{
		ID: connectionid.ID(name), Name: name, Type: "iana-if-type:ethernetCsmacd",
		Owner: interfaceintent.OwnerExternal,
	}
}

func testGateway(members ...Member) Gateway {
	internal := testConnection("eninternal0")
	internal.Roles = interfaceintent.RoleInternal
	internal.IPv4 = &interfaceintent.IPv4{Family: interfaceintent.Family{Enabled: new(true)}}
	internal.IPv6 = &interfaceintent.IPv6{Family: interfaceintent.Family{Enabled: new(true)}}
	gateway := Gateway{InternalIface: internal.Name, Members: members, Connections: []interfaceintent.Connection{internal}}
	for _, member := range members {
		gateway.Connections = append(gateway.Connections, testConnection(member.Iface))
	}
	return gateway
}

// renderedLinkConnection is a link like the free-form instance document's: matched
// by driver, a static IPv4 address with a gateway and an extra source
// address, a DHCPv6 client with every delegation leaf, and one free-form line.
func renderedLinkConnection(name string) interfaceintent.Connection {
	connection := testConnection(name)
	connection.Owner = interfaceintent.OwnerNetworkd
	connection.Link = &interfaceintent.Link{
		Kind: interfaceintent.KindPhysical, Match: interfaceintent.Match{Driver: "igc"},
		HardwareAddress: "02:00:5e:00:53:01",
	}
	connection.IPv4 = &interfaceintent.IPv4{
		Family: interfaceintent.Family{
			Forwarding:  new(true),
			Addresses:   []interfaceintent.Address{{Prefix: netip.MustParsePrefix("203.0.113.2/29"), Purpose: interfaceintent.PurposeLocal}},
			DHCP:        new(false),
			Gateway:     netip.MustParseAddr("203.0.113.1"),
			RouteMetric: new(uint32(10)),
		},
		SourceAddresses: []netip.Addr{netip.MustParseAddr("203.0.113.3")},
	}
	connection.IPv6 = &interfaceintent.IPv6{
		Family:   interfaceintent.Family{Forwarding: new(true), DHCP: new(true), RouteMetric: new(uint32(10))},
		AcceptRA: new(true),
		Delegation: &interfaceintent.Delegation{
			Hint:                  netip.MustParsePrefix("::/56"),
			DUIDType:              "link-layer-time",
			DUID:                  "00:01:2a:5b:3c:4d:02:00:5e:00:53:01",
			WithoutRA:             "solicit",
			UseDelegatedPrefix:    new(false),
			RouterLifetimeSeconds: new(uint32(1800)),
		},
	}
	connection.Networkd = []interfaceintent.UnitFile{{
		Kind: "network",
		Sections: []interfaceintent.UnitSection{{
			Index:   0,
			Name:    "DHCPv6",
			Entries: []interfaceintent.UnitEntry{{Index: 0, Key: "UseDNS", Value: "no"}},
		}},
	}}
	return connection
}

// TestConfigItems_DescribesTheLinkTheDaemonRenders checks the link identity,
// family settings, delegation, and unit sections published for a rendered
// connection. Networkd-owned connections without a link publish hand-authored
// link-files. External connections publish no link-files leaf.
func TestConfigItems_DescribesTheLinkTheDaemonRenders(t *testing.T) {
	t.Parallel()
	webpass := testMember("webpass", "enwebpass0")
	webpass.TableID = 200
	webpass.FwMark = 2
	att := testMember("att", "enatt0")
	monkeybrains := testMember("monkeybrains", "enmbrains0")
	monkeybrains.TableID = 300
	monkeybrains.FwMark = 3
	monkeybrains.FwMarkPrio = 300
	monkeybrains.FromPrio = 57

	gateway := testGateway(webpass, att, monkeybrains)
	gateway.Connections[1] = renderedLinkConnection("enwebpass0")
	gateway.Connections[2].Owner = interfaceintent.OwnerNetworkd
	items, err := ConfigItems(gateway)
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

		{Path: "/ietf-interfaces:interfaces/interface[name='enatt0']/goodkind-mwan-steering:link-files", Value: "hand-authored"},
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
	gateway := testGateway(member)
	gateway.Connections[1].Owner = interfaceintent.OwnerNetworkd
	gateway.Connections[1].Link = &interfaceintent.Link{
		Kind: interfaceintent.KindVLAN,
		VLAN: &interfaceintent.VLAN{Parent: "ensonic0", ID: 101},
	}
	items, err := ConfigItems(gateway)
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
	items, err := ConfigItems(testGateway(member))
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
	items, err := ConfigItems(testGateway(testMember("att", "enatt0")))
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	const attLink = "/ietf-interfaces:interfaces/interface[name='enatt0']"
	want := []Item{
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/type", Value: "iana-if-type:ethernetCsmacd"},
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/enabled", Value: "true"},
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/goodkind-mwan-steering:owner", Value: "external"},
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/ietf-ip:ipv4/enabled", Value: "true"},
		{Path: "/ietf-interfaces:interfaces/interface[name='eninternal0']/ietf-ip:ipv6/enabled", Value: "true"},
		{Path: attLink + "/type", Value: "iana-if-type:ethernetCsmacd"},
		{Path: attLink + "/enabled", Value: "true"},
		{Path: attLink + "/goodkind-mwan-steering:owner", Value: "external"},
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

func TestConfigItems_PublishesNonProviderConnectionIntent(t *testing.T) {
	t.Parallel()
	gateway := testGateway()
	management := testConnection("enmanagement0")
	management.Owner = interfaceintent.OwnerNetworkd
	management.Roles = interfaceintent.RoleManagement
	management.Link = &interfaceintent.Link{Kind: interfaceintent.KindPhysical, Match: interfaceintent.Match{Driver: "igc"}}
	management.IPv4 = &interfaceintent.IPv4{Family: interfaceintent.Family{
		Enabled: new(false), Forwarding: new(false),
		Addresses: []interfaceintent.Address{{Prefix: netip.MustParsePrefix("192.0.2.20/24"), Purpose: interfaceintent.PurposeLocal}},
		DNS:       []netip.Addr{netip.MustParseAddr("192.0.2.53")}, SearchDomains: []string{"example.test"},
	}}
	management.IPv6 = &interfaceintent.IPv6{
		Family: interfaceintent.Family{
			Forwarding: new(true), DNS: []netip.Addr{netip.MustParseAddr("2001:db8::53")},
			SearchDomains: []string{"v6.example.test"},
		},
		ForwardingAddresses: []interfaceintent.ForwardingAddress{{
			Address: netip.MustParseAddr("2001:db8::20"), Delivery: interfaceintent.DeliveryRouted,
		}},
	}
	gateway.Connections = append(gateway.Connections, management)
	gateway.ConnectionIDs = map[string]connectionid.ID{management.Name: "management"}

	items, err := ConfigItems(gateway)
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	served := make(map[string]string, len(items))
	for _, item := range items {
		served[item.Path] = item.Value
	}
	base := "/ietf-interfaces:interfaces/interface[name='enmanagement0']"
	want := map[string]string{
		base + "/type":                                                                                    "iana-if-type:ethernetCsmacd",
		base + "/goodkind-mwan-steering:owner":                                                            "networkd",
		base + "/goodkind-mwan-steering:connection-id":                                                    "management",
		base + "/goodkind-mwan-steering:link-files":                                                       "rendered",
		base + "/ietf-ip:ipv4/enabled":                                                                    "false",
		base + "/ietf-ip:ipv4/forwarding":                                                                 "false",
		base + "/ietf-ip:ipv4/address[ip='192.0.2.20']/prefix-length":                                     "24",
		base + "/ietf-ip:ipv4/goodkind-mwan-steering:resolver/dns":                                        "192.0.2.53",
		base + "/ietf-ip:ipv4/goodkind-mwan-steering:resolver/search":                                     "example.test",
		base + "/ietf-ip:ipv6/forwarding":                                                                 "true",
		base + "/ietf-ip:ipv6/goodkind-mwan-steering:resolver/dns":                                        "2001:db8::53",
		base + "/ietf-ip:ipv6/goodkind-mwan-steering:resolver/search":                                     "v6.example.test",
		base + "/ietf-ip:ipv6/goodkind-mwan-steering:forwarding-address[address='2001:db8::20']/delivery": "routed",
	}
	for path, value := range want {
		if got := served[path]; got != value {
			t.Errorf("%s = %q, want %q", path, got, value)
		}
	}
	for path := range served {
		if strings.HasPrefix(path, base+"/goodkind-mwan-steering:wan") ||
			strings.HasPrefix(path, base+"/goodkind-mwan-steering:steering") {
			t.Errorf("non-provider connection published provider path %s", path)
		}
	}
}

func TestConfigItems_PublishesMWANOwnedLink(t *testing.T) {
	t.Parallel()
	gateway := testGateway()
	parent := testConnection("enparent0")
	owned := testConnection("owned397")
	owned.Owner = interfaceintent.OwnerMWAN
	owned.Link = &interfaceintent.Link{
		Kind: interfaceintent.KindVLAN,
		VLAN: &interfaceintent.VLAN{Parent: parent.Name, ID: 397},
	}
	gateway.Connections = append(gateway.Connections, parent, owned)
	gateway.ConnectionIDs = map[string]connectionid.ID{owned.Name: owned.ID}
	items, err := ConfigItems(gateway)
	if err != nil {
		t.Fatalf("ConfigItems: %v", err)
	}
	served := make(map[string]string, len(items))
	for _, item := range items {
		served[item.Path] = item.Value
	}
	base := "/ietf-interfaces:interfaces/interface[name='owned397']/goodkind-mwan-steering:"
	for path, want := range map[string]string{
		base + "owner":            "mwan",
		base + "connection-id":    "owned397",
		base + "link/vlan/parent": "enparent0",
		base + "link/vlan/id":     "397",
	} {
		if got := served[path]; got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if _, exists := served[base+"link-files"]; exists {
		t.Fatal("MWAN-owned link published networkd ownership")
	}
}

// TestConfigItems_PublishesTheDaemonSettingsItHolds pins the daemon
// container: every present section publishes its leaves under
// /goodkind-mwan-steering:daemon, leaf-list entries are addressed by
// value, and an absent section publishes nothing.
func TestConfigItems_PublishesTheDaemonSettingsItHolds(t *testing.T) {
	t.Parallel()
	gateway := testGateway(testMember("att", "enatt0"))
	gateway.Daemon = DaemonSettings{
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
	items, err := ConfigItems(testGateway(testMember("att", "enatt0")))
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
		return testGateway(member)
	}
	withConnection := func(mutate func(connection *interfaceintent.Connection)) Gateway {
		gateway := testGateway(testMember("att", "enatt0"))
		mutate(&gateway.Connections[1])
		return gateway
	}
	withGroup := func(mutate func(group *GroupSettings)) Gateway {
		gateway := withMember(func(*Member) {})
		mutate(&gateway.Group)
		return gateway
	}
	cases := map[string]Gateway{
		"empty internal link": {InternalIface: "", Members: nil},
		"empty member name":   withMember(func(member *Member) { member.Name = "" }),
		"quote in link":       withConnection(func(connection *interfaceintent.Connection) { connection.Name = "en'att0" }),
		"quote in connection interface key": {
			InternalIface: "eninternal0", Connections: testGateway().Connections, ConnectionIDs: map[string]connectionid.ID{"en'other0": "other"},
		},
		"slash in connection interface key": {
			InternalIface: "eninternal0", Connections: testGateway().Connections, ConnectionIDs: map[string]connectionid.ID{"en/other0": "other"},
		},
		"duplicate link": testGateway(testMember("att", "enatt0"), testMember("webpass", "enatt0")),
		"one translation prefix": withMember(func(member *Member) {
			member.TranslationV6 = &config.IPv6Translation{Mode: config.TranslationNPTv6, NPT: &config.NPTv6Translation{InternalPrefix: internal, ExternalSource: config.PrefixConfigured}}
		}),
		"ipv4 translation prefix": withMember(func(member *Member) {
			member.TranslationV6 = &config.IPv6Translation{Mode: config.TranslationNPTv6, NPT: &config.NPTv6Translation{InternalPrefix: internal, ExternalSource: config.PrefixConfigured}}
			member.TranslationV6.NPT.ExternalPrefix = netip.MustParsePrefix("10.0.0.0/8")
		}),
		"zero weight": withMember(func(member *Member) { member.Weight = 0 }),
		"unknown hash mode": func() Gateway {
			gateway := testGateway(testMember("att", "enatt0"))
			gateway.HashMode = "round-robin"
			return gateway
		}(),
		"zero table id":           withMember(func(member *Member) { member.TableID = 0 }),
		"zero firewall mark":      withMember(func(member *Member) { member.FwMark = 0 }),
		"forced dscp above range": withMember(func(member *Member) { member.ForcedDSCP = 64 }),
		"ipv6 source pin":         withMember(func(member *Member) { member.V4Source = "2001:db8::1" }),
		"ipv6 mapped external": withMember(func(member *Member) {
			member.TranslationV4 = &config.IPv4Translation{Mode: config.TranslationNAPT44, StaticMappings: []config.StaticMapping{
				{External: netip.MustParseAddr("2001:db8::2"), Internal: netip.MustParseAddr("10.250.250.2")},
			}}
		}),
		"missing mapped internal": withMember(func(member *Member) {
			member.TranslationV4 = &config.IPv4Translation{Mode: config.TranslationNAPT44, StaticMappings: []config.StaticMapping{{External: netip.MustParseAddr("198.51.100.2")}}}
		}),
		"external mapped twice": withMember(func(member *Member) {
			member.TranslationV4 = &config.IPv4Translation{Mode: config.TranslationNAPT44, StaticMappings: []config.StaticMapping{
				{External: netip.MustParseAddr("198.51.100.2"), Internal: netip.MustParseAddr("10.250.250.2")},
				{External: netip.MustParseAddr("198.51.100.2"), Internal: netip.MustParseAddr("10.250.250.3")},
			}}
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
		"unknown owner": withConnection(func(connection *interfaceintent.Connection) {
			connection.Owner = interfaceintent.Owner("unknown")
		}),
		"missing type": withConnection(func(connection *interfaceintent.Connection) { connection.Type = "" }),
		"vlan id above range": withConnection(func(connection *interfaceintent.Connection) {
			*connection = renderedLinkConnection("enatt0")
			connection.Link.VLAN = &interfaceintent.VLAN{Parent: "enphys0", ID: 4095}
		}),
		"quote in vlan parent": withConnection(func(connection *interfaceintent.Connection) {
			*connection = renderedLinkConnection("enatt0")
			connection.Link.VLAN = &interfaceintent.VLAN{Parent: "en'phys0", ID: 1}
		}),
		"ipv6 address in the ipv4 family": withConnection(func(connection *interfaceintent.Connection) {
			*connection = renderedLinkConnection("enatt0")
			connection.IPv4.Addresses[0].Prefix = netip.MustParsePrefix("2001:db8::2/64")
		}),
		"ipv4 gateway in the ipv6 family": withConnection(func(connection *interfaceintent.Connection) {
			*connection = renderedLinkConnection("enatt0")
			connection.IPv6.Gateway = netip.MustParseAddr("203.0.113.1")
		}),
		"ipv4 delegation hint": withConnection(func(connection *interfaceintent.Connection) {
			*connection = renderedLinkConnection("enatt0")
			connection.IPv6.Delegation.Hint = netip.MustParsePrefix("10.0.0.0/8")
		}),
		"free-form section with no name": withConnection(func(connection *interfaceintent.Connection) {
			*connection = renderedLinkConnection("enatt0")
			connection.Networkd[0].Sections[0].Name = ""
		}),
		"free-form line with no key": withConnection(func(connection *interfaceintent.Connection) {
			*connection = renderedLinkConnection("enatt0")
			connection.Networkd[0].Sections[0].Entries[0].Key = ""
		}),
		"free-form file of an unknown kind": withConnection(func(connection *interfaceintent.Connection) {
			*connection = renderedLinkConnection("enatt0")
			connection.Networkd[0].Kind = "unit"
		}),
	}
	// A rendered link with every value in range is accepted, so each case
	// above fails on the one value it breaks.
	whole := withConnection(func(connection *interfaceintent.Connection) {
		*connection = renderedLinkConnection("enatt0")
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
