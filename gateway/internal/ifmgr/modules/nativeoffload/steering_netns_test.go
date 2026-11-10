//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ti-mo/conntrack"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/steering"
)

func TestNativeAdmissionOwnedSteering(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	flows := []conntrack.Flow{
		fixture.flow(t, admissionClientV4, admissionRemoteV4, ""),
		fixture.flow(t, admissionClientV6, admissionRemoteV6, ""),
	}
	healthPath := filepath.Join(t.TempDir(), "health.state")
	if err := os.WriteFile(healthPath, []byte(admissionWANID+":healthy\n"+admissionOtherID+":healthy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name string
		mode string
		tier uint8
	}{
		{name: "single-provider", mode: "random", tier: 1},
		{name: "random", mode: "random", tier: 0},
		{name: "source", mode: "source", tier: 0},
		{name: "source-destination", mode: "source-destination", tier: 0},
	} {
		t.Run(entry.name, func(t *testing.T) {
			policy := steering.Config{
				InternalIface: fixture.input.Name, InternalNetV4: "192.0.2.0/24",
				InternalPrefix: "2001:db8:1::/64", OpnsenseEdgeV6: "2001:db8:1::1",
				HashMode: entry.mode, HealthStateFile: healthPath,
				Members: []steering.Member{
					{WANRef: fixture.module.cfg.WANs[1].WANRef, SelectionEnabled: nil, Mark: admissionMark, Tier: 0, Weight: 1},
					{WANRef: fixture.module.cfg.WANs[0].WANRef, SelectionEnabled: nil, Mark: admissionOtherMark, Tier: entry.tier, Weight: 1},
				},
			}
			fixture.checkOwnedSteering(t, policy, flows)
		})
	}
}

func (fixture admissionFixture) checkOwnedSteering(t *testing.T, policy steering.Config, flows []conntrack.Flow) {
	t.Helper()
	writer, err := steering.New(policy)
	if err != nil {
		t.Fatal(err)
	}
	monitorContext, stop := context.WithCancel(fixture.ctx)
	err = writer.Init(monitorContext, fixture.module.Env)
	// Deliberate rule changes must persist until the next explicit reconcile.
	stop()
	if err != nil {
		t.Fatalf("initialize production steering: %v", err)
	}
	fixture.module.cfg.Steering = &policy
	fixture.module.policyGeneration.Add(1)
	if err := writer.Reconcile(fixture.ctx, fixture.module.Log); err != nil {
		t.Fatalf("apply production steering: %v", err)
	}
	fixture.checkSteeringCandidates(t, flows)
	for _, chain := range []string{"prerouting", "forward"} {
		before := fixture.module.rules.Generation()
		command := exec.CommandContext(fixture.ctx, "nft", "add", "rule", "inet", "mwan_steer", chain, "ct", "state", "established", "drop")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("change owned %s policy: %v: %s", chain, err, output)
		}
		generation, err := fixture.module.rules.Barrier(fixture.ctx)
		if err != nil || generation <= before {
			t.Fatalf("owned policy change did not invalidate certification: before=%d after=%d error=%v", before, generation, err)
		}
		for _, flow := range flows {
			observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
			fixture.requireRejected(t, observation, EligibilityPolicyUnavailable)
		}
		if err := writer.Reconcile(fixture.ctx, fixture.module.Log); err != nil {
			t.Fatalf("restore production steering: %v", err)
		}
		fixture.checkSteeringCandidates(t, flows)
	}
}

func (fixture admissionFixture) checkSteeringCandidates(t *testing.T, flows []conntrack.Flow) {
	t.Helper()
	if _, err := fixture.module.rules.Barrier(fixture.ctx); err != nil {
		t.Fatalf("checkpoint applied steering policy: %v", err)
	}
	for _, flow := range flows {
		observation := admissionObservation(t, fixture.sender, fixture.input, flow.TupleOrig)
		fixture.requireCandidate(t, observation, flow, flow.TupleOrig, 0, fixture.output, fixture.peer)
	}
}
