//go:build linux && netns

package bpf

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/nptpolicy"
)

const nativePolicyTestTimeout = 10 * time.Second

func TestNativePacketPolicyRejectsForeignFilters(t *testing.T) {
	link := isolatePinTestNetwork(t)
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), nativePolicyTestTimeout)
	defer cancel()
	indices := []int{link.Attrs().Index}
	if err := InspectNativePolicy(ctx, indices); err != nil {
		t.Fatalf("kernel-default queues failed inspection: %v", err)
	}
	if err := ensureClsact(link); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []Direction{Ingress, Egress} {
		filter := &netlink.MatchAll{FilterAttrs: netlink.FilterAttrs{
			LinkIndex: link.Attrs().Index, Parent: filterParent(direction),
			Handle: 1, Priority: 1, Protocol: unix.ETH_P_ALL,
		}, Actions: []netlink.Action{&netlink.GenericAction{ActionAttrs: netlink.ActionAttrs{Action: netlink.TC_ACT_SHOT}}}}
		if err := netlink.FilterAdd(filter); err != nil {
			t.Fatal(err)
		}
		if err := InspectNativePolicy(ctx, indices); err == nil {
			t.Fatalf("native inspection accepted a foreign %s drop filter", direction)
		}
		if err := netlink.FilterDel(filter); err != nil {
			t.Fatal(err)
		}
		if err := InspectNativePolicy(ctx, indices); err != nil {
			t.Fatalf("native policy did not recover after exact %s filter removal: %v", direction, err)
		}
	}
	queue := &netlink.Prio{QdiscAttrs: netlink.QdiscAttrs{LinkIndex: link.Attrs().Index, Parent: netlink.HANDLE_ROOT, Handle: netlink.MakeHandle(1, 0)}, Bands: 1}
	if err := netlink.QdiscReplace(queue); err != nil {
		t.Fatal(err)
	}
	if err := InspectNativePolicy(ctx, indices); err == nil {
		t.Fatal("native inspection accepted an explicit root queue policy")
	}
}

func TestNativePacketPolicyNPTIdentity(t *testing.T) {
	link := isolatePinTestNetwork(t)
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), nativePolicyTestTimeout)
	defer cancel()
	indices := []int{link.Attrs().Index}
	policies := pinTestPolicies(link)
	first, err := newTranslator(mountPinTestBPFFS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	if _, err := first.Reconcile(policies); err != nil {
		t.Fatal(err)
	}
	if err := first.InspectNativePolicy(ctx, indices, policies); err != nil {
		t.Fatalf("current NPT programs and map failed inspection: %v", err)
	}
	if err := InspectNativePolicy(ctx, indices); err == nil {
		t.Fatal("native inspection trusted an NPT label without its owner")
	}
	second, err := newTranslator(mountPinTestBPFFS(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	if _, err := second.Reconcile(policies); err != nil {
		t.Fatal(err)
	}
	if err := first.InspectNativePolicy(ctx, indices, policies); err == nil {
		t.Fatal("native inspection accepted identically labeled programs from another owner")
	}
	if err := second.InspectNativePolicy(ctx, indices, policies); err != nil {
		t.Fatalf("replacement owner could not verify its real programs and map: %v", err)
	}
	changedPair := policies[0].Pairs[0]
	changedPair.External = netip.MustParsePrefix("2001:db8:2::/48")
	changed := []InterfacePolicy{{IfIndex: link.Attrs().Index, Pairs: []nptpolicy.PrefixPair{changedPair}}}
	if _, err := first.Reconcile(changed); err != nil {
		t.Fatal(err)
	}
	if err := first.InspectNativePolicy(ctx, indices, policies); err == nil {
		t.Fatal("native inspection accepted map bytes from another applied prefix policy")
	}
	if _, err := first.Reconcile(policies); err != nil {
		t.Fatal(err)
	}
	if err := first.InspectNativePolicy(ctx, indices, policies); err != nil {
		t.Fatalf("restored NPT program and policy identity did not recover: %v", err)
	}
}

func TestNativePacketPolicyKernelDefaultMQ(t *testing.T) {
	isolatePinTestNetwork(t)
	ctx, cancel := context.WithTimeout(t.Context(), nativePolicyTestTimeout)
	defer cancel()
	link := &netlink.Tuntap{
		LinkAttrs: netlink.LinkAttrs{Name: "native-mq", TxQLen: 1000}, Mode: netlink.TUNTAP_MODE_TAP,
		Flags: netlink.TUNTAP_MULTI_QUEUE | netlink.TUNTAP_NO_PI, Queues: 2,
	}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatalf("create the real multiqueue TAP fixture through /dev/net/tun: %v", err)
	}
	t.Cleanup(func() {
		for _, descriptor := range link.Fds {
			if err := descriptor.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	actual, err := netlink.LinkByName(link.Name)
	if err != nil {
		t.Fatal(err)
	}
	queues, err := netlink.QdiscList(actual)
	if err != nil {
		t.Fatal(err)
	}
	multiqueue := false
	for _, queue := range queues {
		multiqueue = multiqueue || queue.Type() == "mq" && queue.Attrs().Handle == 0
	}
	if !multiqueue {
		t.Fatalf("kernel did not create the required default mq topology: %+v", queues)
	}
	if err := InspectNativePolicy(ctx, []int{actual.Attrs().Index}); err != nil {
		t.Fatalf("kernel-default mq/fq_codel topology failed inspection: %v", err)
	}
}
