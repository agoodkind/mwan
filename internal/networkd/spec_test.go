package networkd_test

import (
	"net/netip"
	"testing"

	"goodkind.io/mwan/internal/networkd"
)

// staticLinkSpec is a link like webpass: matched by driver, a static IPv4
// address, a DHCPv6 client with a delegation, and free-form sections the
// caller chooses.
func staticLinkSpec(files []networkd.File) networkd.Spec {
	return networkd.Spec{
		Name:            "enwebpass0",
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
		},
		IPv6: &networkd.FamilyV6{
			Family:   networkd.Family{Forwarding: new(true), DHCP: new(true)},
			AcceptRA: new(true),
			Delegation: &networkd.Delegation{
				Hint:     netip.MustParsePrefix("::/56"),
				DUIDType: "link-layer-time",
				DUID:     "00:01:2a:5b:3c:4d:02:00:5e:00:53:01",
			},
		},
		Files: files,
	}
}

// networkSection builds one free-form section of the .network file.
func networkSection(name string, key string, value string) []networkd.File {
	return []networkd.File{{
		Kind: networkd.FileNetwork,
		Sections: []networkd.Section{{
			Index:   0,
			Name:    name,
			Entries: []networkd.Entry{{Index: 0, Key: key, Value: value}},
		}},
	}}
}

func TestValidateRejectsAKeySetByBothLayers(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		files []networkd.File
		want  string
	}{
		"delegation hint": {
			files: networkSection("DHCPv6", "PrefixDelegationHint", "::/60"),
			want:  "networkd section DHCPv6 key PrefixDelegationHint is set by the delegation hint leaf; remove one",
		},
		// Both dhcp leaves fold into one DHCP= line, so the free-form key
		// collides with whichever family the entry typed.
		"folded dhcp": {
			files: networkSection("Network", "DHCP", "yes"),
			want:  "networkd section Network key DHCP is set by the ipv6 dhcp leaf; remove one",
		},
		"static address": {
			files: networkSection("Network", "Address", "203.0.113.3/29"),
			want:  "networkd section Network key Address is set by the ipv4 address leaf; remove one",
		},
		"driver match in the link file": {
			files: []networkd.File{{
				Kind: networkd.FileLink,
				Sections: []networkd.Section{{
					Index:   0,
					Name:    "Match",
					Entries: []networkd.Entry{{Index: 0, Key: "Driver", Value: "e1000e"}},
				}},
			}},
			want: "networkd section Match key Driver is set by the match driver leaf; remove one",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			err := networkd.Validate(staticLinkSpec(tc.files))
			if err == nil {
				t.Fatal("Validate accepted a key set by both layers")
			}
			if err.Error() != tc.want {
				t.Fatalf("Validate error = %q, want %q", err, tc.want)
			}
		})
	}
}

func TestValidateAcceptsAKeyNoTypedLeafSets(t *testing.T) {
	t.Parallel()

	cases := map[string][]networkd.File{
		"a key under a typed heading": networkSection("DHCPv6", "UseDNS", "no"),
		"a heading no leaf names":     networkSection("DHCPv4", "UseDNS", "no"),
		// The typed table places a key in one file kind, so the same key in
		// another kind is the network manager's to judge rather than a
		// doubled leaf.
		"a typed key in another file": {{
			Kind: networkd.FileNetdev,
			Sections: []networkd.Section{{
				Index:   0,
				Name:    "Match",
				Entries: []networkd.Entry{{Index: 0, Key: "Driver", Value: "igc"}},
			}},
		}},
		"no free-form content": nil,
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if err := networkd.Validate(staticLinkSpec(files)); err != nil {
				t.Fatalf("Validate rejected a key no typed leaf sets: %v", err)
			}
		})
	}
}

// TestValidateOccupiesTheLeaseMetricOnlyWithoutAGateway pins that a route
// metric occupies the lease client's key only on a family with no gateway:
// a family with a gateway carries its metric on its static route, so the
// lease client's key is free for the operator to set.
func TestValidateOccupiesTheLeaseMetricOnlyWithoutAGateway(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		section string
		key     string
		want    string
	}{
		"ipv4": {
			section: "DHCPv4",
			key:     "RouteMetric",
			want:    "networkd section DHCPv4 key RouteMetric is set by the ipv4 route-metric leaf; remove one",
		},
		"ipv6": {
			section: "IPv6AcceptRA",
			key:     "RouteMetric",
			want:    "networkd section IPv6AcceptRA key RouteMetric is set by the ipv6 route-metric leaf; remove one",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			leased := leasedLinkSpec()
			leased.Files = networkSection(tc.section, tc.key, "100")
			err := networkd.Validate(leased)
			if err == nil {
				t.Fatal("Validate accepted a lease metric set by both layers")
			}
			if err.Error() != tc.want {
				t.Fatalf("Validate error = %q, want %q", err, tc.want)
			}

			static := staticLinkSpec(networkSection(tc.section, tc.key, "100"))
			static.IPv6.Gateway = netip.MustParseAddr("2001:db8::1")
			static.IPv6.RouteMetric = new(10)
			if err := networkd.Validate(static); err != nil {
				t.Fatalf("Validate rejected a lease metric on a family with a gateway: %v", err)
			}
		})
	}
}

// TestValidateReadsOnlyTheLeavesTheSpecSets pins that an unset typed leaf
// occupies nothing: a spec that types no delegation may carry the delegation
// keys free-form, which is how a shape gains typed leaves later without a
// renderer change.
func TestValidateReadsOnlyTheLeavesTheSpecSets(t *testing.T) {
	t.Parallel()

	spec := staticLinkSpec(networkSection("DHCPv6", "PrefixDelegationHint", "::/60"))
	spec.IPv6.Delegation = nil
	if err := networkd.Validate(spec); err != nil {
		t.Fatalf("Validate rejected a free-form key whose typed leaf is unset: %v", err)
	}

	spec = staticLinkSpec(networkSection("Network", "DHCP", "yes"))
	spec.IPv4.DHCP = nil
	spec.IPv6.DHCP = nil
	if err := networkd.Validate(spec); err != nil {
		t.Fatalf("Validate rejected a free-form DHCP key with neither dhcp leaf set: %v", err)
	}
}
