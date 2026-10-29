//go:build linux && cgo && nativeoffload && netns

package nativeoffload

import (
	"context"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/offload"
)

func TestNativeAdmissionBlockedPolicyRecovery(t *testing.T) {
	if !nativeCaptureNamespace(t) {
		return
	}
	fixture := newAdmissionFixture(t, config.TranslationNative)
	module := fixture.module
	if !module.refreshPolicy(fixture.ctx) {
		t.Fatal("the real kernel baseline did not certify")
	}
	if err := netlink.QdiscAdd(&netlink.Clsact{QdiscAttrs: netlink.QdiscAttrs{
		LinkIndex: fixture.output.Index, Handle: netlink.MakeHandle(0xffff, 0), Parent: netlink.HANDLE_CLSACT,
	}}); err != nil {
		t.Fatalf("add recovery fixture clsact: %v", err)
	}
	filter := &netlink.MatchAll{FilterAttrs: netlink.FilterAttrs{
		LinkIndex: fixture.output.Index, Parent: netlink.HANDLE_MIN_EGRESS,
		Handle: 1, Priority: 1, Protocol: unix.ETH_P_ALL,
	}, Actions: []netlink.Action{&netlink.GenericAction{ActionAttrs: netlink.ActionAttrs{Action: netlink.TC_ACT_SHOT}}}}
	if err := netlink.FilterAdd(filter); err != nil {
		t.Fatalf("install the real recovery fixture drop: %v", err)
	}
	if module.refreshPolicy(fixture.ctx) || !module.Stats().PolicyBlocked {
		t.Fatal("the real egress drop did not block native policy")
	}
	startAdmissionPolicyLoop(t, fixture)
	previous := module.forwarding.Generation()
	if err := netlink.FilterDel(filter); err != nil {
		t.Fatalf("remove the exact recovery fixture drop: %v", err)
	}
	awaitAdmissionPolicyRecovery(t, fixture, previous)
}

func startAdmissionPolicyLoop(t *testing.T, fixture admissionFixture) {
	t.Helper()
	module := fixture.module
	ctx, cancel := context.WithCancel(fixture.ctx)
	var err error
	module.events, err = offload.NewConntrackEvents(ctx, 16, nativeCaptureSocketBytes)
	if err != nil {
		cancel()
		t.Fatalf("start real recovery conntrack observations: %v", err)
	}
	t.Cleanup(func() {
		if err := module.events.Close(); err != nil {
			t.Errorf("close recovery conntrack observations: %v", err)
		}
	})
	module.cfg.Settings.CounterInterval = admissionTimeout
	module.cfg.Settings.ResyncInterval = admissionTimeout
	done := make(chan struct{})
	go func() {
		defer close(done)
		module.run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(admissionTimeout):
			t.Error("the recovery admission loop did not stop after cancellation")
		}
	})
}

func awaitAdmissionPolicyRecovery(t *testing.T, fixture admissionFixture, previous uint64) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		control := fixture.module.Stats()
		if control.Failure != nil {
			t.Fatalf("native admission failed during policy recovery: %v", control.Failure)
		}
		if fixture.module.forwarding.Generation() > previous && !control.PolicyBlocked {
			return
		}
		select {
		case <-ticker.C:
		case <-fixture.ctx.Done():
			t.Fatalf("observed drop removal did not restore native policy: %+v", control)
		}
	}
}
