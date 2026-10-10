package bgp_test

import (
	"net/netip"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/bgp"
)

func TestNewSessionRejectsInvalidConfiguration(t *testing.T) {
	valid := bgp.SessionConfig{
		Name:             "invalid",
		RouterID:         netip.MustParseAddr(sessionRouterID),
		LocalASN:         sessionASN,
		RemoteASN:        externalPeerASN,
		PeerAddress:      netip.MustParseAddr("2001:db8:ffff::2"),
		PeerPort:         179,
		KeepaliveSeconds: keepaliveSeconds,
		HoldSeconds:      holdSeconds,
	}
	hop := netip.MustParseAddr("2001:db8:ffff::1")
	exportPrefix := netip.MustParsePrefix("2001:db8:a::/48")
	cases := []struct {
		name    string
		mutate  func(*bgp.SessionConfig)
		wantErr string
	}{
		{"missing name", func(c *bgp.SessionConfig) { c.Name = "" }, "name is required"},
		{"zero local ASN", func(c *bgp.SessionConfig) { c.LocalASN = 0 }, "local ASN is required"},
		{"zero remote ASN", func(c *bgp.SessionConfig) { c.RemoteASN = 0 }, "remote ASN is required"},
		{"invalid peer address", func(c *bgp.SessionConfig) { c.PeerAddress = netip.Addr{} }, "peer address"},
		{"zero peer port", func(c *bgp.SessionConfig) { c.PeerPort = 0 }, "peer port is required"},
		{"IPv4 import prefix", func(c *bgp.SessionConfig) {
			c.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("192.0.2.0/24"), MinLength: 24, MaxLength: 24}}
		}, "is not an IPv6 prefix"},
		{"import minimum below the prefix length", func(c *bgp.SessionConfig) {
			c.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 24, MaxLength: 48}}
		}, "inconsistent length bounds"},
		{"import maximum below the minimum", func(c *bgp.SessionConfig) {
			c.Import = []bgp.ImportRule{{Prefix: netip.MustParsePrefix("2001:db8::/32"), MinLength: 48, MaxLength: 40}}
		}, "inconsistent length bounds"},
		{"IPv4 export prefix", func(c *bgp.SessionConfig) {
			c.Export = []bgp.ExportRule{{Prefix: netip.MustParsePrefix("192.0.2.0/24"), Mode: bgp.ExportAlways, NextHop: hop}}
		}, "is not an IPv6 prefix"},
		{"duplicate export prefix", func(c *bgp.SessionConfig) {
			c.Export = []bgp.ExportRule{
				{Prefix: exportPrefix, Mode: bgp.ExportAlways, NextHop: hop},
				{Prefix: exportPrefix, Mode: bgp.ExportBackup, NextHop: hop},
			}
		}, "configured more than once"},
		{"unspecified router ID", func(c *bgp.SessionConfig) {
			c.RouterID = netip.IPv4Unspecified()
		}, "is not a usable IPv4 address"},
		{"hold timer below three seconds", func(c *bgp.SessionConfig) {
			c.KeepaliveSeconds = 1
			c.HoldSeconds = 2
		}, "hold timer 2 is outside the range"},
		{"hold timer above 16 bits", func(c *bgp.SessionConfig) {
			c.HoldSeconds = 65536
		}, "hold timer 65536 is outside the range"},
		{"zero route metric with kernel tables", func(c *bgp.SessionConfig) {
			c.Interface = "bgpext0"
			c.Tables = []int{100}
		}, "route metric 0 collides"},
		{"kernel default route metric with kernel tables", func(c *bgp.SessionConfig) {
			c.Interface = "bgpext0"
			c.Tables = []int{100}
			c.RouteMetric = 1024
		}, "route metric 1024 collides"},
		{"prepend on an iBGP session", func(c *bgp.SessionConfig) {
			c.RemoteASN = c.LocalASN
			c.Export = []bgp.ExportRule{{Prefix: exportPrefix, Mode: bgp.ExportAlways, PrependCount: 1, NextHop: hop}}
		}, "prepends the local ASN on an iBGP session"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			cfg := valid
			testCase.mutate(&cfg)
			session, err := bgp.NewSession(cfg, sessionLogger())
			if err == nil {
				t.Fatalf("NewSession returned session %v, want an error containing %q", session, testCase.wantErr)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Errorf("NewSession error = %q, want an error containing %q", err, testCase.wantErr)
			}
		})
	}
}
