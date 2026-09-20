package main

import (
	"reflect"
	"testing"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkd"
)

// TestRenamedLinksNamesALinkWhoseNameTheFileDoesNotAsk pins the one case
// the daemon warns about: a rendered .link file changed, the device it
// matches is already present, and the device carries a different name. The
// kernel refuses to rename a link that is up and udev reads the file only
// when the device appears, so the warning is the whole effect until the
// next reboot.
func TestRenamedLinksNamesALinkWhoseNameTheFileDoesNotAsk(t *testing.T) {
	t.Parallel()

	byDriver := staticLinkSpecForMain("enwebpass0", networkd.Match{Driver: "igc", HardwareAddress: ""})
	byAddress := staticLinkSpecForMain("enmbrains0", networkd.Match{Driver: "", HardwareAddress: "02:00:5E:00:53:02"})
	specs := []networkd.Spec{byDriver, byAddress}
	linkChanges := []networkd.Change{
		{File: "20-enwebpass0.link", Kind: networkd.FileLink, Interface: "enwebpass0", Removed: false},
		{File: "20-enmbrains0.link", Kind: networkd.FileLink, Interface: "enmbrains0", Removed: false},
	}

	cases := map[string]struct {
		changes []networkd.Change
		live    []netif.LinkIdentity
		want    []linkRename
	}{
		"a driver match on a device with another name": {
			changes: linkChanges,
			live: []netif.LinkIdentity{
				{Name: "eth2", HardwareAddress: "00:e2:69:66:8b:5a", Driver: "igc"},
			},
			want: []linkRename{{Current: "eth2", Wanted: "enwebpass0"}},
		},
		"an address match compared without regard to case": {
			changes: linkChanges,
			live: []netif.LinkIdentity{
				{Name: "eth3", HardwareAddress: "02:00:5e:00:53:02", Driver: "virtio_net"},
			},
			want: []linkRename{{Current: "eth3", Wanted: "enmbrains0"}},
		},
		"a device already carrying the name": {
			changes: linkChanges,
			live: []netif.LinkIdentity{
				{Name: "enwebpass0", HardwareAddress: "00:e2:69:66:8b:5a", Driver: "igc"},
				{Name: "enmbrains0", HardwareAddress: "02:00:5e:00:53:02", Driver: "virtio_net"},
			},
			want: nil,
		},
		"a device not yet present": {
			changes: linkChanges,
			live:    nil,
			want:    nil,
		},
		"a changed .network file and a removed .link file": {
			changes: []networkd.Change{
				{File: "20-enwebpass0.network", Kind: networkd.FileNetwork, Interface: "enwebpass0", Removed: false},
				{File: "20-enold0.link", Kind: networkd.FileLink, Interface: "", Removed: true},
			},
			live: []netif.LinkIdentity{
				{Name: "eth2", HardwareAddress: "00:e2:69:66:8b:5a", Driver: "igc"},
			},
			want: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := renamedLinks(tc.changes, specs, tc.live)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("renamedLinks = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// staticLinkSpecForMain is a link with the given identity and nothing else
// the rename comparison reads.
func staticLinkSpecForMain(name string, match networkd.Match) networkd.Spec {
	return networkd.Spec{
		Name:            name,
		TableID:         200,
		Match:           match,
		HardwareAddress: "",
		VLAN:            nil,
		IPv4:            nil,
		IPv6:            nil,
		Files:           nil,
	}
}
