package ovs

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

func TestOpenFlowOmitsEntriesWithMissingLeases(t *testing.T) {
	fixed := []ovsconfig.Action{{ID: 1, Name: "output", Argument: "wan0"}}
	wantFixed := slices.Clone(fixed)
	leased := []ovsconfig.Action{{ID: 1, Name: "set_field", Argument: "${dhcpv4:uplink-a}->ip_src"}}
	config := ovsconfig.OpenFlowConfig{
		Flows: []ovsconfig.Flow{
			{Name: "fixed", Actions: fixed},
			{Name: "leased-action", Actions: leased},
			{
				Name:    "delegated-match",
				Match:   []ovsconfig.Match{{Field: "ipv6_dst", Value: "${dhcpv6-pd:uplink-a}"}},
				Actions: fixed,
			},
		},
		Groups: []ovsconfig.Group{
			{
				ID:      1,
				Type:    ovsconfig.GroupIndirect,
				Buckets: []ovsconfig.Bucket{{ID: 7, Actions: leased}},
			},
			{
				ID:   2,
				Type: ovsconfig.GroupSelect,
				Buckets: []ovsconfig.Bucket{
					{ID: 7, Actions: leased},
					{ID: 8, Actions: fixed},
				},
			},
			{
				ID:      3,
				Type:    ovsconfig.GroupSelect,
				Buckets: []ovsconfig.Bucket{{ID: 7, Actions: leased}},
			},
		},
	}
	values := leaseValues{now: time.Unix(1_700_000_000, 0)}
	got, err := values.openFlow(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}

	if len(got.Flows) != 1 || got.Flows[0].Name != "fixed" ||
		!slices.Equal(got.Flows[0].Actions, wantFixed) {
		t.Errorf("flows = %+v, want only the fixed flow with actions %+v", got.Flows, wantFixed)
	}

	if len(got.Groups) != 3 {
		t.Fatalf("groups = %+v, want one indirect and two select groups", got.Groups)
	}

	if indirect := got.Groups[0]; indirect.ID != 1 || len(indirect.Buckets) != 0 {
		t.Errorf("indirect group = %+v, want group 1 without buckets", indirect)
	}

	selected := got.Groups[1]
	if selected.ID != 2 || len(selected.Buckets) != 1 || selected.Buckets[0].ID != 8 ||
		!slices.Equal(selected.Buckets[0].Actions, wantFixed) {
		t.Errorf(
			"select group = %+v, want group 2 with only bucket 8 and actions %+v",
			selected,
			wantFixed,
		)
	}

	if selected := got.Groups[2]; selected.ID != 3 || len(selected.Buckets) != 0 {
		t.Errorf("select group = %+v, want group 3 without buckets", selected)
	}
}

func TestOpenFlowBindsCurrentLeases(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	validUntil := now.Add(time.Hour)
	const connectionID = "uplink-a"
	values := leaseValues{
		now: now,
		connections: map[string]wanstate.ConnectionState{
			connectionID: {
				IPv4: wanstate.FamilyState{
					Assignments: []interfaceintent.Assignment{
						{
							Kind:       interfaceintent.AssignmentDHCPv4,
							Value:      netip.MustParsePrefix("192.0.2.23/24"),
							Valid:      true,
							ValidUntil: &validUntil,
						},
					},
				},
				IPv6: wanstate.FamilyState{
					Assignments: []interfaceintent.Assignment{
						{
							Kind:       interfaceintent.AssignmentDHCPv6IAPD,
							Value:      netip.MustParsePrefix("2001:db8:1::/48"),
							Valid:      true,
							ValidUntil: &validUntil,
						},
					},
				},
			},
		},
	}
	config := ovsconfig.OpenFlowConfig{
		Flows: []ovsconfig.Flow{
			{
				Name:    "delegated-match",
				Match:   []ovsconfig.Match{{Field: "ipv6_dst", Value: "${dhcpv6-pd:uplink-a}"}},
				Actions: []ovsconfig.Action{{ID: 1, Name: "output", Argument: "wan0"}},
			},
		},
		Groups: []ovsconfig.Group{
			{
				ID:   1,
				Type: ovsconfig.GroupSelect,
				Buckets: []ovsconfig.Bucket{
					{
						ID: 7,
						Actions: []ovsconfig.Action{
							{
								ID:       1,
								Name:     "clone",
								Argument: "set_field:${dhcpv4:uplink-a}->ip_src,output:wan0",
							},
						},
					},
				},
			},
		},
	}
	got, err := values.openFlow(t.Context(), config)
	if err != nil {
		t.Fatal(err)
	}

	wantMatch := []ovsconfig.Match{{Field: "ipv6_dst", Value: "2001:db8:1::/48"}}
	if len(got.Flows) != 1 || !slices.Equal(got.Flows[0].Match, wantMatch) {
		t.Errorf("flows = %+v, want a flow with match %+v", got.Flows, wantMatch)
	}

	wantActions := []ovsconfig.Action{
		{
			ID:       1,
			Name:     "clone",
			Argument: "set_field:192.0.2.23->ip_src,output:wan0",
		},
	}
	if len(got.Groups) != 1 || len(got.Groups[0].Buckets) != 1 ||
		!slices.Equal(got.Groups[0].Buckets[0].Actions, wantActions) {
		t.Errorf(
			"groups = %+v, want a group with one bucket and actions %+v",
			got.Groups,
			wantActions,
		)
	}
}
