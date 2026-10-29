//go:build linux && netns

package npt

import (
	"context"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func TestAppliedNPTNativePacketPolicy(t *testing.T) {
	fixture := newPolicyKernelFixture(t)
	ctx, cancel := context.WithTimeout(fixture.ctx, 10*time.Second)
	defer cancel()
	inspect := fixture.module.Env.InspectNativePacketPolicy
	if inspect == nil {
		t.Fatal("NPT initialization did not publish the packet policy inspector")
	}
	indices := []int{fixture.wan.Attrs().Index}
	if err := inspect(ctx, indices); err != nil {
		t.Fatalf("applied NPT owner could not certify its actual programs and map: %v", err)
	}
	filter := &netlink.MatchAll{FilterAttrs: netlink.FilterAttrs{
		LinkIndex: fixture.wan.Attrs().Index, Parent: netlink.HANDLE_MIN_EGRESS,
		Handle: 1, Priority: 1, Protocol: unix.ETH_P_ALL,
	}, Actions: []netlink.Action{&netlink.GenericAction{ActionAttrs: netlink.ActionAttrs{Action: netlink.TC_ACT_SHOT}}}}
	if err := netlink.FilterAdd(filter); err != nil {
		t.Fatal(err)
	}
	if err := inspect(ctx, indices); err == nil {
		t.Fatal("applied NPT readiness authorized an additional egress drop")
	}
	if err := netlink.FilterDel(filter); err != nil {
		t.Fatal(err)
	}
	if err := inspect(ctx, indices); err != nil {
		t.Fatalf("exact filter removal did not restore NPT packet policy certification: %v", err)
	}
}
