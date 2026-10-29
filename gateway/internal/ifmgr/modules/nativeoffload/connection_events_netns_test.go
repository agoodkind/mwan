//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/offload"
)

func TestNativeAdmissionConntrackUpdateVerification(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	events, err := offload.NewConntrackEvents(fixture.ctx, 32, nativeCaptureSocketBytes)
	if err != nil {
		t.Fatal(err)
	}
	fixture.module.events = events
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, addresses := range [][2]string{{admissionClientV4, admissionRemoteV4}, {admissionClientV6, admissionRemoteV6}} {
		flow := fixture.flow(t, addresses[0], addresses[1], "")
		observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
		spec, guards, current, wan, eligibility, err := fixture.module.candidate(fixture.ctx, observation)
		if err != nil || eligibility != EligibilityReady {
			t.Fatalf("initial live candidate failed: decision=%d error=%v", eligibility, err)
		}
		entry := newAdmittedFlow(current, spec, guards, wan, observation, clock.Real{}.Now())
		changed := flow
		changed.Mark = admissionOtherMark
		if err := fixture.tracking.Update(changed); err != nil {
			t.Fatal(err)
		}
		old := awaitAdmissionConntrackPolicy(t, fixture, events, flow.ID, admissionOtherMark, false)
		if sameConnectionPolicy(flow, old.Flow) {
			t.Fatal("actual older mark update did not differ from the admitted policy")
		}
		if err := fixture.tracking.Update(flow); err != nil {
			t.Fatal(err)
		}
		awaitAdmissionConntrackPolicy(t, fixture, events, flow.ID, admissionMark, false)
		evidence := fixture.module.evidenceFor(fixture.module.currentEvidenceEpoch())
		fresh, decision, err := fixture.module.verifyConnectionUpdate(fixture.ctx, old, entry, evidence)
		if err != nil || decision != EligibilityReady || fresh.ID != flow.ID || fresh.Mark != admissionMark {
			t.Fatalf("older kernel notification rejected current admitted policy: current=%+v decision=%d error=%v", fresh, decision, err)
		}
		evidence = requireConntrackEvidenceRefresh(t, fixture, old, entry, evidence)
		if err := fixture.tracking.Update(changed); err != nil {
			t.Fatal(err)
		}
		actual := awaitAdmissionConntrackPolicy(t, fixture, events, flow.ID, admissionOtherMark, false)
		fresh, decision, err = fixture.module.verifyConnectionUpdate(fixture.ctx, actual, entry, evidence)
		if err != nil || decision != EligibilityUnsupported || fresh.Mark != admissionOtherMark {
			t.Fatalf("current changed policy accepted: current=%+v decision=%d error=%v", fresh, decision, err)
		}
		if err := fixture.tracking.Delete(flow); err != nil {
			t.Fatal(err)
		}
		destroyed := awaitAdmissionConntrackPolicy(t, fixture, events, flow.ID, admissionOtherMark, true)
		fresh, decision, err = fixture.module.verifyConnectionUpdate(fixture.ctx, destroyed, entry, evidence)
		if !errors.Is(err, syscall.ENOENT) || decision != EligibilityUnsupported {
			t.Fatalf("deleted kernel connection passed fresh verification: current=%+v decision=%d error=%v", fresh, decision, err)
		}
	}
}

func requireConntrackEvidenceRefresh(t *testing.T, fixture admissionFixture, event offload.ConntrackEvent, entry *admittedFlow, stale *evidenceCheck) *evidenceCheck {
	t.Helper()
	family := unix.AF_INET
	if entry.flow.TupleOrig.IP.SourceAddress.Is6() {
		family = unix.AF_INET6
	}
	neighbor, present := fixture.neighbors[family]
	if !present {
		t.Fatalf("the real admission fixture omitted neighbor family %d", family)
	}
	changed := neighbor
	changed.HardwareAddr = fixture.otherPeer.HardwareAddr
	previous := fixture.module.forwarding.Generation()
	if err := netlink.NeighSet(&changed); err != nil {
		t.Fatal(err)
	}
	awaitConntrackForwardingGeneration(t, fixture, previous)
	previous = fixture.module.forwarding.Generation()
	if err := netlink.NeighSet(&neighbor); err != nil {
		t.Fatal(err)
	}
	awaitConntrackForwardingGeneration(t, fixture, previous)
	current, decision, err := fixture.module.verifyConnectionUpdate(fixture.ctx, event, entry, stale)
	if !errors.Is(err, syscall.ESTALE) || decision != EligibilityStale {
		t.Fatalf("obsolete forwarding evidence passed otherwise restored mapping verification: current=%+v decision=%d error=%v", current, decision, err)
	}
	fresh := fixture.module.evidenceFor(fixture.module.currentEvidenceEpoch())
	current, decision, err = fixture.module.verifyConnectionUpdate(fixture.ctx, event, entry, fresh)
	if err != nil || decision != EligibilityReady || current.ID != entry.flow.ID || current.Mark != entry.flow.Mark {
		t.Fatalf("current evidence rejected the restored admitted mapping: current=%+v decision=%d error=%v", current, decision, err)
	}
	return fresh
}

func awaitConntrackForwardingGeneration(t *testing.T, fixture admissionFixture, previous uint64) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for fixture.module.forwarding.Generation() <= previous {
		select {
		case <-ticker.C:
		case <-fixture.ctx.Done():
			t.Fatalf("real neighbor update did not advance forwarding generation %d: %v", previous, fixture.ctx.Err())
		}
	}
	if err := fixture.module.forwarding.Err(); err != nil {
		t.Fatalf("real neighbor update lost forwarding evidence: %v", err)
	}
}

func awaitAdmissionConntrackPolicy(t *testing.T, fixture admissionFixture, events *offload.ConntrackEvents, id, mark uint32, destroyed bool) offload.ConntrackEvent {
	t.Helper()
	for {
		select {
		case event, open := <-events.Events:
			if !open {
				t.Fatalf("live conntrack stream closed: %v", events.Err())
			}
			if event.Flow.ID == id && event.Flow.Mark == mark && event.Destroyed == destroyed {
				return event
			}
		case err := <-events.Errors:
			t.Fatalf("live conntrack event failure: %v", err)
		case <-fixture.ctx.Done():
			t.Fatalf("target conntrack update did not arrive: id=%d mark=%d destroyed=%v error=%v", id, mark, destroyed, fixture.ctx.Err())
		}
	}
}
