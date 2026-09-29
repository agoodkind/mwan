package wanconfig

import (
	"strings"
	"testing"

	"goodkind.io/mwan/internal/interfaceintent"
)

func TestConfigItemsPublishesMWANOwnedDHCPv6RequestModes(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name        string
		ipv6        *interfaceintent.IPv6
		wantAddress bool
		wantPrefix  bool
	}{
		{
			name: "address only",
			ipv6: &interfaceintent.IPv6{
				Family: interfaceintent.Family{DHCP: new(true)},
				DHCPv6: &interfaceintent.DHCPv6{
					DUID: "00:01:2a:5b:3c:4d:02:00:5e:00:53:01", IANAIAID: new(uint32(41)),
					RequestAddress: new(true), RequestPrefix: new(false), WithoutRA: "solicit", UseDNS: new(false),
				},
			},
			wantAddress: true,
		},
		{
			name: "prefix only",
			ipv6: &interfaceintent.IPv6{
				Family: interfaceintent.Family{DHCP: new(true)},
				Delegation: &interfaceintent.Delegation{
					DUID: "00:01:2a:5b:3c:4d:02:00:5e:00:53:01", IAID: new(uint32(41)), WithoutRA: "solicit",
				},
			},
			wantPrefix: true,
		},
		{
			name: "combined",
			ipv6: &interfaceintent.IPv6{
				Family: interfaceintent.Family{DHCP: new(true)},
				Delegation: &interfaceintent.Delegation{
					DUID: "00:01:2a:5b:3c:4d:02:00:5e:00:53:01", IAID: new(uint32(41)), WithoutRA: "solicit",
				},
				DHCPv6: &interfaceintent.DHCPv6{IANAIAID: new(uint32(41)), RequestAddress: new(true)},
			},
			wantAddress: true, wantPrefix: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			gateway := testGateway()
			owned := testConnection("endhcp6")
			owned.Owner = interfaceintent.OwnerMWAN
			owned.Link = &interfaceintent.Link{Kind: interfaceintent.KindPhysical}
			owned.IPv6 = testCase.ipv6
			gateway.Connections = append(gateway.Connections, owned)
			items, err := ConfigItems(gateway)
			if err != nil {
				t.Fatalf("ConfigItems: %v", err)
			}
			served := make(map[string]string, len(items))
			for _, item := range items {
				served[item.Path] = item.Value
			}
			base := "/ietf-interfaces:interfaces/interface[name='endhcp6']/ietf-ip:ipv6/goodkind-mwan-steering:"
			if served[base+"dhcp"] != "true" {
				t.Fatalf("served DHCPv6 setting = %q", served[base+"dhcp"])
			}
			if testCase.wantAddress && served[base+"dhcpv6-client/address-iaid"] != "41" {
				t.Fatalf("served address IAID = %q", served[base+"dhcpv6-client/address-iaid"])
			}
			if testCase.wantPrefix && served[base+"delegation/iaid"] != "41" {
				t.Fatalf("served prefix IAID = %q", served[base+"delegation/iaid"])
			}
		})
	}
}

func TestConfigItemsRejectsInvalidMWANOwnedDHCPv6Requests(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		client interfaceintent.DHCPv6
		want   string
	}{
		{name: "neither association", client: interfaceintent.DHCPv6{RequestAddress: new(false), RequestPrefix: new(false), WithoutRA: "solicit"}, want: "must request an address or prefix"},
		{name: "DNS installation", client: interfaceintent.DHCPv6{IANAIAID: new(uint32(41)), RequestAddress: new(true), WithoutRA: "solicit", UseDNS: new(true)}, want: "use-dns requires resolver ownership"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			gateway := testGateway()
			owned := testConnection("endhcp6")
			owned.Owner = interfaceintent.OwnerMWAN
			owned.Link = &interfaceintent.Link{Kind: interfaceintent.KindPhysical}
			testCase.client.DUID = "00:01:2a:5b:3c:4d:02:00:5e:00:53:01"
			owned.IPv6 = &interfaceintent.IPv6{
				Family: interfaceintent.Family{DHCP: new(true)}, DHCPv6: &testCase.client,
			}
			gateway.Connections = append(gateway.Connections, owned)
			_, err := ConfigItems(gateway)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("ConfigItems error = %v, want %q", err, testCase.want)
			}
		})
	}
}
