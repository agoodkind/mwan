package wanstate_test

import (
	"reflect"
	"testing"

	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/wanstate"
)

func TestSetRoutingRecordsTheFailedDependency(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	store.SetConnections([]interfaceintent.Connection{{ID: "tunnel", Name: "tun6in4"}})
	passes := []wanstate.MemberRouting{
		{V6Reason: "probe pending"},
		{V6Ready: true},
		{V6Reason: "probe failed"},
		{V6Reason: "probe failed"},
		{V6Reason: "underlay not ready"},
	}
	for _, routing := range passes {
		store.SetRouting(0, map[string]wanstate.MemberRouting{"tunnel": routing})
	}

	type change struct{ previous, current, reason string }
	var got []change
	for _, transition := range store.Snapshot().Connections["tunnel"].Recent {
		if transition.Operation != "evaluate-routing" || transition.Dependency != "wan-routes" {
			t.Fatalf("transition = %+v, want a routing evaluation", transition)
		}
		if transition.Family == "ipv4" {
			t.Fatalf("the family without a readiness change recorded %+v", transition)
		}
		got = append(got, change{previous: transition.Previous, current: transition.Current, reason: transition.Reason})
	}
	want := []change{
		{previous: "not-ready", current: "ready", reason: "routing readiness changed"},
		{previous: "ready", current: "not-ready", reason: "probe failed"},
		{previous: "not-ready", current: "not-ready", reason: "underlay not ready"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("IPv6 routing transitions = %+v, want %+v", got, want)
	}
	if reason := store.Snapshot().Routing["tunnel"].V6Reason; reason != "underlay not ready" {
		t.Fatalf("current IPv6 reason = %q, want the last failed dependency", reason)
	}
}

func TestSetRoutingRecordsNoTransitionForARepeatedStateAndReason(t *testing.T) {
	t.Parallel()
	store := wanstate.New()
	store.SetConnections([]interfaceintent.Connection{{ID: "isp", Name: "enisp0"}})
	type change struct{ family, previous, current, reason string }
	steps := []struct {
		routing wanstate.MemberRouting
		want    []change
	}{
		{routing: wanstate.MemberRouting{V4Ready: true, V6Reason: "gateway unavailable"}},
		{
			routing: wanstate.MemberRouting{V4Reason: "probe failed", V6Reason: "gateway unavailable"},
			want:    []change{{family: "ipv4", previous: "ready", current: "not-ready", reason: "probe failed"}},
		},
		{
			routing: wanstate.MemberRouting{V4Reason: "gateway unavailable", V6Reason: "gateway unavailable"},
			want:    []change{{family: "ipv4", previous: "not-ready", current: "not-ready", reason: "gateway unavailable"}},
		},
		{
			routing: wanstate.MemberRouting{V4Ready: true, V6Reason: "gateway unavailable"},
			want:    []change{{family: "ipv4", previous: "not-ready", current: "ready", reason: "routing readiness changed"}},
		},
	}
	var want []change
	for _, step := range steps {
		want = append(want, step.want...)
		for range 3 {
			store.SetRouting(0, map[string]wanstate.MemberRouting{"isp": step.routing})
			var got []change
			for _, transition := range store.Snapshot().Connections["isp"].Recent {
				got = append(got, change{family: transition.Family, previous: transition.Previous, current: transition.Current, reason: transition.Reason})
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("transitions after %+v = %+v, want %+v", step.routing, got, want)
			}
		}
	}
}
