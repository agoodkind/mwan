//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"errors"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/offload"
)

func TestNativeAdmissionTrafficControl(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	flow := fixture.flow(t, admissionClientV4, admissionRemoteV4, "")
	observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	var err error
	fixture.module.events, err = offload.NewConntrackEvents(fixture.ctx, 16, nativeCaptureSocketBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := fixture.module.events.Close(); err != nil {
			t.Error(err)
		}
	})
	guard := fixture.module.evidenceFor(fixture.module.currentEvidenceEpoch()).guard
	if err := guard.Check(); err != nil {
		t.Fatalf("unchanged real kernel evidence rejected its guard: %v", err)
	}
	previous := fixture.module.forwarding.Generation()
	if err := netlink.QdiscAdd(&netlink.Clsact{QdiscAttrs: netlink.QdiscAttrs{
		LinkIndex: fixture.output.Index, Handle: netlink.MakeHandle(0xffff, 0), Parent: netlink.HANDLE_CLSACT,
	}}); err != nil {
		t.Fatal(err)
	}
	fixture.awaitTrafficPolicyChange(t, previous)
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	guard = fixture.module.evidenceFor(fixture.module.currentEvidenceEpoch()).guard
	filter := &netlink.MatchAll{FilterAttrs: netlink.FilterAttrs{
		LinkIndex: fixture.output.Index, Parent: netlink.HANDLE_MIN_EGRESS,
		Handle: 1, Priority: 1, Protocol: unix.ETH_P_ALL,
	}, Actions: []netlink.Action{&netlink.GenericAction{ActionAttrs: netlink.ActionAttrs{Action: netlink.TC_ACT_SHOT}}}}
	previous = fixture.module.forwarding.Generation()
	if err := netlink.FilterAdd(filter); err != nil {
		t.Fatal(err)
	}
	fixture.awaitTrafficPolicyChange(t, previous)
	if err := guard.Check(); !errors.Is(err, syscall.ESTALE) {
		t.Fatalf("real egress drop did not invalidate the issued native guard: %v", err)
	}
	fixture.requireRejected(t, observation, EligibilityPolicyUnavailable)
	previous = fixture.module.forwarding.Generation()
	if err := netlink.FilterDel(filter); err != nil {
		t.Fatal(err)
	}
	fixture.awaitTrafficPolicyChange(t, previous)
	fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
}

func (fixture admissionFixture) awaitTrafficPolicyChange(t *testing.T, previous uint64) {
	t.Helper()
	for fixture.module.forwarding.Generation() <= previous {
		select {
		case <-fixture.module.forwarding.Changes:
		case err := <-fixture.module.forwarding.Errors:
			t.Fatalf("traffic-control observations failed: %v", err)
		case <-fixture.ctx.Done():
			t.Fatalf("traffic-control mutation did not invalidate native evidence: %v", fixture.ctx.Err())
		}
	}
	if err := fixture.module.forwarding.Err(); err != nil {
		t.Fatal(err)
	}
}
