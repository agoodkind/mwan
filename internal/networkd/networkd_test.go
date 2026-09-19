package networkd_test

import (
	"maps"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/networkd"
)

// leasedLinkSpec is a link like monkeybrains: matched by hardware address,
// both families leased with a route metric and no gateway, and a delegation
// carrying every leaf the model names.
func leasedLinkSpec() networkd.Spec {
	return networkd.Spec{
		Name:            "enmbrains0",
		TableID:         300,
		Match:           networkd.Match{Driver: "", HardwareAddress: "02:00:5e:00:53:02"},
		HardwareAddress: "",
		VLAN:            nil,
		IPv4: &networkd.FamilyV4{
			Family: networkd.Family{
				Forwarding:  new(true),
				Addresses:   nil,
				DHCP:        new(true),
				Gateway:     netip.Addr{},
				RouteMetric: new(5000),
			},
			SourceAddresses: nil,
		},
		IPv6: &networkd.FamilyV6{
			Family: networkd.Family{
				Forwarding:  new(true),
				Addresses:   nil,
				DHCP:        new(true),
				Gateway:     netip.Addr{},
				RouteMetric: new(5000),
			},
			AcceptRA: new(true),
			Delegation: &networkd.Delegation{
				Hint:                  netip.MustParsePrefix("::/56"),
				DUIDType:              "link-layer-time",
				DUID:                  "00:01:2a:5b:3c:4d:02:00:5e:00:53:02",
				WithoutRA:             "solicit",
				UseDelegatedPrefix:    new(true),
				RouterLifetimeSeconds: new(1800),
			},
		},
		Files: nil,
	}
}

// vlanLinkSpec is a provider handed off on a tagged VLAN: no device match,
// a parent and a tag, and both families leased.
func vlanLinkSpec() networkd.Spec {
	return networkd.Spec{
		Name:            "ensonic0.101",
		TableID:         600,
		Match:           networkd.Match{Driver: "", HardwareAddress: ""},
		HardwareAddress: "",
		VLAN:            &networkd.VLAN{Parent: "ensonic0", ID: 101},
		IPv4: &networkd.FamilyV4{
			Family: networkd.Family{
				Forwarding:  new(true),
				Addresses:   nil,
				DHCP:        new(true),
				Gateway:     netip.Addr{},
				RouteMetric: nil,
			},
			SourceAddresses: nil,
		},
		IPv6: &networkd.FamilyV6{
			Family: networkd.Family{
				Forwarding:  new(true),
				Addresses:   nil,
				DHCP:        new(true),
				Gateway:     netip.Addr{},
				RouteMetric: nil,
			},
			AcceptRA:   new(true),
			Delegation: nil,
		},
		Files: nil,
	}
}

// readExpected reads one checked-in rendering.
func readExpected(t *testing.T, name string) string {
	t.Helper()
	want, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	return string(want)
}

// assertFiles checks that the render produced exactly the named files, that
// each opens with the marker, and that each matches its checked-in file.
func assertFiles(t *testing.T, files map[string]string, expected map[string]string) {
	t.Helper()
	got := slices.Sorted(maps.Keys(files))
	names := slices.Sorted(maps.Keys(expected))
	if !slices.Equal(got, names) {
		t.Fatalf("Render produced %v, want %v", got, names)
	}
	for name, testdataName := range expected {
		content := files[name]
		if !strings.HasPrefix(content, networkd.Marker+"\n") {
			t.Fatalf("%s does not open with the marker:\n%s", name, content)
		}
		if want := readExpected(t, testdataName); content != want {
			t.Fatalf("%s mismatch:\ngot:\n%s\nwant:\n%s", name, content, want)
		}
	}
}

func TestRenderWritesAStaticLinkWithADelegation(t *testing.T) {
	t.Parallel()

	files, err := networkd.Render(staticLinkSpec(nil))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertFiles(t, files, map[string]string{
		"20-enwebpass0.link":    "20-enwebpass0.link",
		"20-enwebpass0.network": "20-enwebpass0.network",
	})
}

// TestRenderWritesALeasedLinkMetricOntoTheLease pins where a leased family's
// route metric lands: on the DHCPv4 client for IPv4 and on the router
// advertisement client for IPv6, because a leased family contributes no
// static route to carry it.
func TestRenderWritesALeasedLinkMetricOntoTheLease(t *testing.T) {
	t.Parallel()

	files, err := networkd.Render(leasedLinkSpec())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertFiles(t, files, map[string]string{
		"20-enmbrains0.link":    "20-enmbrains0.link",
		"20-enmbrains0.network": "20-enmbrains0.network",
	})
}

// TestRenderAppendsFreeFormSectionsAfterTheTypedLines covers the three
// places a free-form section can land: under a heading the typed layer
// emits once, where the network manager reads a repeated heading as one
// section; as its own section when no typed leaf names the heading; and as
// its own section when the typed layer repeats the heading, because a
// repeated [Route] is a list of routes rather than one section.
func TestRenderAppendsFreeFormSectionsAfterTheTypedLines(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		files []networkd.File
		want  string
	}{
		"a key under a typed heading": {
			files: networkSection("DHCPv6", "UseDNS", "no"),
			want:  "merge-typed-heading.network",
		},
		"a heading no leaf names": {
			files: networkSection("DHCPv4", "UseDNS", "no"),
			want:  "merge-new-heading.network",
		},
		"a heading the typed layer repeats": {
			files: []networkd.File{{
				Kind: networkd.FileNetwork,
				Sections: []networkd.Section{{
					Index: 0,
					Name:  "Route",
					Entries: []networkd.Entry{
						{Index: 0, Key: "Destination", Value: "198.51.100.0/24"},
						{Index: 1, Key: "Gateway", Value: "203.0.113.1"},
					},
				}},
			}},
			want: "merge-repeated-heading.network",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			files, err := networkd.Render(staticLinkSpec(tc.files))
			if err != nil {
				t.Fatalf("Render: %v", err)
			}
			assertFiles(t, files, map[string]string{
				"20-enwebpass0.link":    "20-enwebpass0.link",
				"20-enwebpass0.network": tc.want,
			})
		})
	}
}

// TestRenderWritesAVLANAsANetdevAndANetwork pins that a VLAN child produces
// its own two files and nothing named for its parent: the line that creates
// the VLAN lands in the parent's file, which is written from the parent's
// own specification.
func TestRenderWritesAVLANAsANetdevAndANetwork(t *testing.T) {
	t.Parallel()

	files, err := networkd.Render(vlanLinkSpec())
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	assertFiles(t, files, map[string]string{
		"20-ensonic0.101.netdev":  "20-ensonic0.101.netdev",
		"20-ensonic0.101.network": "20-ensonic0.101.network",
	})
}

// TestRenderRejectsAHardwareAddressOnAVLAN pins that a VLAN carrying a
// hardware address is refused rather than written: the address lands in a
// .link file, a VLAN has no device match to put in it, and a .link file with
// no match applies to every device.
func TestRenderRejectsAHardwareAddressOnAVLAN(t *testing.T) {
	t.Parallel()

	spec := vlanLinkSpec()
	spec.HardwareAddress = "02:00:5e:00:53:03"
	_, err := networkd.Render(spec)
	if err == nil {
		t.Fatal("Render wrote a .link file for a VLAN")
	}
	const want = "ensonic0.101: hardware-address is set on a VLAN, which has no .link file to carry it"
	if err.Error() != want {
		t.Fatalf("Render error = %q, want %q", err, want)
	}
}
