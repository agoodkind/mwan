//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"testing"

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
		awaitAdmissionConntrackPolicy(t, fixture, events, flow.ID, admissionOtherMark, true)
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
