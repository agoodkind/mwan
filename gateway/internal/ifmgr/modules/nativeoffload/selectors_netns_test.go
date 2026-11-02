//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"errors"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/ti-mo/conntrack"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/offload"
)

func TestNativeSelectorCandidateChanges(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	fixture.module.cfg.Settings.ObservationMaxAge = time.Second
	for _, addresses := range [][2]string{{admissionClientV4, admissionRemoteV4}, {admissionClientV6, admissionRemoteV6}} {
		flow := fixture.flow(t, addresses[0], addresses[1], "")
		original := selectorPacketObservation(t, fixture, flow.TupleReply, admissionTrafficClass, nativeCaptureFlowLabel)
		entry := selectorFixtureEntry(t, fixture, original)
		label := uint32(0)
		if flow.TupleReply.IP.SourceAddress.Is6() {
			label = nativeCaptureFlowLabel + 1
		}
		changed := selectorPacketObservation(t, fixture, flow.TupleReply, admissionTrafficClass+4, label)
		evidence := selectorFixtureEvidence(t, fixture, 16)
		candidate := fixture.module.selectorCandidate(fixture.ctx, entry, changed, evidence)
		if candidate == nil || candidate.guards == entry.guards || !sameNativeMapping(candidate.spec, entry.spec) || candidate.guards[1].FlowLabel != label || candidate.guards[1].TrafficClass != admissionTrafficClass+4 {
			t.Fatalf("fresh selectors did not preserve the real kernel mapping: candidate=%+v previous=%+v", candidate, entry)
		}
		if entry.guards[1].TrafficClass != admissionTrafficClass || entry.guards[1].FlowLabel != original.FlowLabel {
			t.Fatal("candidate validation changed published selectors before hardware installation")
		}
		changed.at = changed.at.Add(-2 * fixture.module.cfg.Settings.ObservationMaxAge)
		if fixture.module.selectorCandidate(fixture.ctx, entry, changed, evidence) != nil {
			t.Fatal("selector replacement accepted an expired packet observation")
		}
	}
}

func TestNativeSelectorCandidateRejectsRouteAndEvidenceChanges(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	fixture.module.cfg.Settings.ObservationMaxAge = time.Second
	flow := fixture.flow(t, admissionClientV4, admissionRemoteV4, "")
	original := selectorPacketObservation(t, fixture, flow.TupleReply, admissionTrafficClass, 0)
	entry := selectorFixtureEntry(t, fixture, original)
	evidence := selectorFixtureEvidence(t, fixture, 16)
	const newClass = admissionTrafficClass + 4
	rule := netlink.NewRule()
	rule.Family, rule.Priority, rule.Table = unix.AF_INET, admissionRoutePriority-1, admissionRouteTable
	rule.IifName, rule.Mark, rule.IPProto, rule.Tos = fixture.output.Name, admissionMark, unix.IPPROTO_TCP, newClass
	previous := fixture.module.forwarding.Generation()
	if err := netlink.RuleAdd(rule); err != nil {
		t.Fatalf("add actual changed-selector route policy: %v", err)
	}
	fixture.awaitTrafficPolicyChange(t, previous)
	changed := selectorPacketObservation(t, fixture, flow.TupleReply, newClass, 0)
	if !errors.Is(evidence.guard.Check(), syscall.ESTALE) || fixture.module.selectorCandidate(fixture.ctx, entry, changed, evidence) != nil {
		t.Fatal("selector replacement accepted evidence from before the route mutation")
	}
	path, usable, err := netif.ForwardPathLookup(fixture.ctx, fixture.module.Log, netif.PacketSelector{
		Source:         netip.AddrPortFrom(flow.TupleReply.IP.SourceAddress, flow.TupleReply.Proto.SourcePort),
		Destination:    netip.AddrPortFrom(flow.TupleReply.IP.DestinationAddress, flow.TupleReply.Proto.DestinationPort),
		InputInterface: fixture.output.Index, Protocol: unix.IPPROTO_TCP, Mark: admissionMark, TrafficClass: newClass,
	})
	if err != nil || !usable || path.Link.Attrs().Index != fixture.output.Index {
		t.Fatalf("real DSCP route did not select the changed output: usable=%v path=%+v error=%v", usable, path, err)
	}
	fresh := fixture.module.evidenceFor(fixture.module.currentEvidenceEpoch())
	if fixture.module.selectorCandidate(fixture.ctx, entry, changed, fresh) != nil {
		t.Fatal("selector replacement accepted a different kernel forwarding decision")
	}
}

func TestNativeSelectorCandidateRejectsLostObservations(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	fixture.module.cfg.Settings.ObservationMaxAge = time.Second
	flow := fixture.flow(t, admissionClientV6, admissionRemoteV6, "")
	original := selectorPacketObservation(t, fixture, flow.TupleReply, admissionTrafficClass, nativeCaptureFlowLabel)
	entry := selectorFixtureEntry(t, fixture, original)
	evidence := selectorFixtureEvidence(t, fixture, 1)
	for index := range 2 {
		created := conntrack.NewFlow(unix.IPPROTO_UDP, conntrack.StatusConfirmed|conntrack.StatusSeenReply|conntrack.StatusAssured,
			netip.MustParseAddr(admissionClientV4), netip.MustParseAddr(admissionRemoteV4), uint16(20000+index), 5204, admissionFlowTimeout, admissionMark)
		if err := fixture.tracking.Create(created); err != nil {
			t.Fatalf("create real conntrack overflow input: %v", err)
		}
	}
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for fixture.module.events.Err() == nil {
		select {
		case <-ticker.C:
		case <-fixture.ctx.Done():
			t.Fatal("the real conntrack source did not report observation loss")
		}
	}
	changed := selectorPacketObservation(t, fixture, flow.TupleReply, admissionTrafficClass, nativeCaptureFlowLabel+1)
	if fixture.module.selectorCandidate(fixture.ctx, entry, changed, evidence) != nil {
		t.Fatal("selector replacement accepted lost connection observations")
	}
}

func selectorPacketObservation(t *testing.T, fixture admissionFixture, tuple conntrack.Tuple, trafficClass uint8, flowLabel uint32) packetObservation {
	t.Helper()
	frame := admissionSelectorFrame(t, fixture.peer, fixture.output, tuple, trafficClass, flowLabel)
	observed, err := offload.ObserveNativeFrames([][]byte{frame})
	if err != nil || len(observed) != 1 || !observed[0].Eligible || observed[0].Tuple != tuple || observed[0].TrafficClass != trafficClass {
		t.Fatalf("public Rust packet decoding rejected the selector fixture: observations=%+v error=%v", observed, err)
	}
	return packetObservation{NativeObservation: observed[0], EffectiveNetworkLength: observed[0].NetworkLength, ifindex: fixture.output.Index, at: clock.Real{}.Now()}
}

func selectorFixtureEntry(t *testing.T, fixture admissionFixture, observation packetObservation) *admittedFlow {
	t.Helper()
	spec, guards, flow, wan, decision, err := fixture.module.candidate(fixture.ctx, observation)
	if err != nil || decision != EligibilityReady {
		t.Fatalf("real selector baseline was not eligible: decision=%d error=%v", decision, err)
	}
	return newAdmittedFlow(flow, spec, guards, wan, observation, clock.Real{}.Now())
}

func selectorFixtureEvidence(t *testing.T, fixture admissionFixture, capacity int) *evidenceCheck {
	t.Helper()
	if fixture.module.events == nil {
		var err error
		fixture.module.events, err = offload.NewConntrackEvents(fixture.ctx, capacity, nativeCaptureSocketBytes)
		if err != nil {
			t.Fatalf("start real selector conntrack evidence: %v", err)
		}
		t.Cleanup(func() {
			if err := fixture.module.events.Close(); err != nil {
				t.Errorf("close selector conntrack evidence: %v", err)
			}
		})
	}
	return fixture.module.evidenceFor(fixture.module.currentEvidenceEpoch())
}
