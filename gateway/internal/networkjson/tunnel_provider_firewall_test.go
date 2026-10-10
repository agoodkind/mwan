package networkjson_test

import (
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/firewall"
)

func TestTunnelProviderFirewallProjection(t *testing.T) {
	loaded := decodeTunnelProvider(t)
	wantPermit := firewall.TransportPermit{
		InputInterface: "enisp0", Family: firewall.IPv4, Source: netip.MustParsePrefix("198.51.100.1/32"),
		Destination: netip.Prefix{}, Protocol: firewall.ProtocolIPv6InIPv4, SourcePort: 0, DestinationPort: 0,
	}
	found := false
	for _, permit := range loaded.Firewall.LocalPermits {
		if permit.InputInterface == "tun6in4" {
			t.Fatalf("tunnel provider has a local permit: %+v", permit)
		}
		found = found || permit == wantPermit
	}
	if !found {
		t.Fatalf("local permits = %+v, want %+v", loaded.Firewall.LocalPermits, wantPermit)
	}
	wantPaths := []firewall.ForwardingPath{
		{InternalInterface: "enmwanbr0", ExternalInterface: "enisp0", IPv4: true, IPv6: true},
		{InternalInterface: "enmwanbr0", ExternalInterface: "tun6in4", IPv4: false, IPv6: true},
	}
	if !reflect.DeepEqual(loaded.Firewall.Paths, wantPaths) {
		t.Fatalf("forwarding paths = %+v, want %+v", loaded.Firewall.Paths, wantPaths)
	}
	for _, provider := range loaded.Firewall.Providers {
		if provider.Interface == "tun6in4" && (provider.MasqueradeIPv4 || provider.Mark != 2) {
			t.Fatalf("tunnel firewall provider = %+v, want mark 2 without IPv4 masquerade", provider)
		}
	}
	wrongFamily := loaded.Firewall
	wrongFamily.LocalPermits = []firewall.TransportPermit{wantPermit}
	wrongFamily.LocalPermits[0].Family = firewall.IPv6
	wrongFamily.LocalPermits[0].Source = netip.Prefix{}
	if err := wrongFamily.Validate(); err == nil || !strings.Contains(err.Error(), "requires the IPv4 family") {
		t.Fatalf("Validate accepted protocol 41 in the IPv6 family: %v", err)
	}
}

func TestCompileWritesNoIPv4MarkRuleForTheTunnelProvider(t *testing.T) {
	configuration := decodeTunnelProvider(t).Firewall
	configuration.Providers = slices.Clone(configuration.Providers)
	for index := range configuration.Providers {
		configuration.Providers[index].ForcedDSCP = uint8(8 + index)
	}
	configuration.PinnedProvider = "tun6in4"
	configuration.PinnedSourceIPv4 = netip.MustParseAddr("192.0.2.2")
	configuration.PinnedSourceIPv6 = netip.MustParseAddr("2001:db8:b01:fe::2")
	configuration.PinnedSourcePort, configuration.PinnedDestinationPort = 51820, 51821
	rules, err := firewall.Compile(configuration)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	text := rules.String()
	for _, line := range strings.Split(text, "\n") {
		ipv4Match := strings.Contains(line, "ip saddr") || strings.Contains(line, "ip daddr") || strings.Contains(line, "ip dscp")
		if ipv4Match && strings.HasSuffix(line, "meta mark set 2") {
			t.Fatalf("the ruleset marks IPv4 packets with the tunnel mark: %q", line)
		}
	}
	for _, want := range []string{
		`iifname "enmwanbr0" ip dscp 8 ct state new meta mark set 1`,
		`iifname "enmwanbr0" ip6 dscp 8 ct state new meta mark set 1`,
		`iifname "enmwanbr0" ip6 dscp 9 ct state new meta mark set 2`,
		`ip6 daddr @isp_pinned_v6 meta mark set 2`,
		`ip6 saddr 2001:db8:b01:fe::2/128 udp sport 51820 udp dport 51821 ct state new meta mark set 2`,
	} {
		if !strings.Contains(text, "  "+want+"\n") {
			t.Fatalf("the ruleset lacks %q:\n%s", want, text)
		}
	}
}
