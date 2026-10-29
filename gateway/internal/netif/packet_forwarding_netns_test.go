//go:build linux && netns

package netif_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	mdnetlink "github.com/mdlayher/netlink"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

const (
	forwardPathRootUIDPriority             = 50
	forwardPathNamespaceNexthopID   uint32 = 1
	forwardPathResilientGroupID     uint32 = 11
	forwardPathResilientBucketCount uint16 = 8
	// The pinned x/sys does not expose these Linux nexthop UAPI constants.
	forwardPathResilientGroupType        uint16 = 1
	forwardPathResilientGroupAttribute   uint16 = 12
	forwardPathResilientBucketsAttribute uint16 = 1
)

func TestForwardPathLookupMatchesForwardingWithRootUIDRules(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_UID_CHILD") {
		return
	}
	var kernel unix.Utsname
	if err := unix.Uname(&kernel); err != nil {
		t.Fatalf("inspect runtime kernel identity: %v", err)
	}
	t.Logf("runtime kernel: release=%s version=%s machine=%s", unix.ByteSliceToString(kernel.Release[:]),
		unix.ByteSliceToString(kernel.Version[:]), unix.ByteSliceToString(kernel.Machine[:]))
	for index, family := range []int{unix.AF_INET, unix.AF_INET6} {
		t.Run(fmt.Sprintf("family%d", family), func(t *testing.T) {
			fixture := newForwardPathFixture(t, family, fmt.Sprintf("fu%d", index))
			fixture.selector.Mark = 0
			forwarding := netlink.NewRule()
			forwarding.Family, forwarding.Priority, forwarding.Table = family, forwardPathRootUIDPriority+1, forwardPathTable
			forwarding.IifName = fixture.input.Attrs().Name
			if err := netlink.RuleAdd(forwarding); err != nil {
				t.Fatalf("install the unmarked forwarding policy: %v", err)
			}
			endpoints := isolateForwardPathEndpoints(t, fixture)
			requireForwardPathPacket(t, fixture, endpoints, 1, "before-root-rule")
			rule := netlink.NewRule()
			rule.Family, rule.Priority, rule.Table = family, forwardPathRootUIDPriority, unix.RT_TABLE_MAIN
			rule.UIDRange = &netlink.RuleUIDRange{Start: 0, End: 0}
			if err := netlink.RuleAdd(rule); err != nil {
				t.Fatalf("install the root UID policy: %v", err)
			}
			requireForwardPathPacket(t, fixture, endpoints, 0, "after-root-rule")
		})
	}
}

func TestForwardPathLookupUsesConcurrentCallingNamespaces(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_NAMESPACES_CHILD") {
		return
	}
	baseline := newForwardPathFixture(t, unix.AF_INET, "fn")
	baseline.selector.Mark = 0
	installForwardPathNamespaceNexthop(t, baseline, 0)
	assertForwardPath(t, baseline, baseline.selector, 0)
	var ready sync.WaitGroup
	ready.Add(len(baseline.outputs))
	start := make(chan struct{})
	var finished sync.WaitGroup
	finished.Add(len(baseline.outputs))
	for expected := range baseline.outputs {
		go func() {
			defer finished.Done()
			t.Run(fmt.Sprintf("output%d", expected), func(t *testing.T) {
				arrived := false
				defer func() {
					if !arrived {
						ready.Done()
					}
				}()
				// A goroutine that exits with its thread locked destroys that thread.
				runtime.LockOSThread()
				if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
					t.Fatalf("create the calling network namespace: %v", err)
				}
				fixture := newForwardPathFixture(t, unix.AF_INET, "fn")
				fixture.selector.Mark = 0
				if fixture.selector != baseline.selector {
					t.Fatalf("namespace selectors differ: got %+v, want %+v", fixture.selector, baseline.selector)
				}
				installForwardPathNamespaceNexthop(t, fixture, expected)
				ready.Done()
				arrived = true
				select {
				case <-start:
				case <-t.Context().Done():
					t.Fatal(t.Context().Err())
				}
				assertForwardPath(t, fixture, fixture.selector, expected)
			})
		}()
	}
	ready.Wait()
	close(start)
	finished.Wait()
}

func installForwardPathNamespaceNexthop(t *testing.T, fixture forwardPathFixture, expected int) {
	t.Helper()
	attributes := mdnetlink.NewAttributeEncoder()
	attributes.Uint32(unix.NHA_ID, forwardPathNamespaceNexthopID)
	attributes.Uint32(unix.NHA_OIF, uint32(fixture.outputs[expected].Attrs().Index))
	attributes.Bytes(unix.NHA_GATEWAY, fixture.gateways[expected].To4())
	addForwardPathNexthop(t, unix.Nhmsg{Family: unix.AF_INET, Protocol: unix.RTPROT_BGP}, attributes)
	replaceForwardPathNexthopRoute(t, fixture.selector, forwardPathNamespaceNexthopID)
}

func TestForwardPathLookupRequiresResilientGroupObservation(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_RESILIENT_GROUP_CHILD") {
		return
	}
	fixture := newForwardPathFixture(t, unix.AF_INET, "fg")
	selector := fixture.selector
	selector.Mark = 0
	for index, output := range fixture.outputs {
		attributes := mdnetlink.NewAttributeEncoder()
		attributes.Uint32(unix.NHA_ID, uint32(index+1))
		attributes.Uint32(unix.NHA_OIF, uint32(output.Attrs().Index))
		attributes.Bytes(unix.NHA_GATEWAY, fixture.gateways[index].To4())
		addForwardPathNexthop(t, unix.Nhmsg{Family: unix.AF_INET, Protocol: unix.RTPROT_BGP}, attributes)
	}
	settings := netif.NewProcSysctlRunner(slog.Default(), false)
	if err := settings.Set(t.Context(), "net.ipv4.nexthop_compat_mode", "0"); err != nil {
		t.Fatal(err)
	}
	members, err := binary.Append(nil, binary.NativeEndian, []unix.NexthopGrp{{Id: 1}, {Id: 2}})
	if err != nil {
		t.Fatalf("encode the resilient nexthop members: %v", err)
	}
	attributes := mdnetlink.NewAttributeEncoder()
	attributes.Uint32(unix.NHA_ID, forwardPathResilientGroupID)
	attributes.Bytes(unix.NHA_GROUP, members)
	attributes.Uint16(unix.NHA_GROUP_TYPE, forwardPathResilientGroupType)
	attributes.Nested(forwardPathResilientGroupAttribute, func(nested *mdnetlink.AttributeEncoder) error {
		nested.Uint16(forwardPathResilientBucketsAttribute, forwardPathResilientBucketCount)
		return nil
	})
	addForwardPathNexthop(t, unix.Nhmsg{}, attributes)
	replaceForwardPathNexthopRoute(t, selector, forwardPathResilientGroupID)
	path, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), selector)
	var requirement *netif.EgressObservationRequiredError
	if usable || !errors.As(err, &requirement) || requirement.Reason != netif.EgressObservationNexthopGroup ||
		requirement.NexthopID != forwardPathResilientGroupID || path.Link == nil {
		t.Fatalf("resilient group did not require actual egress: path=%+v usable=%v err=%v", path, usable, err)
	}
	for index, output := range fixture.outputs {
		if path.Link.Attrs().Index != output.Attrs().Index {
			continue
		}
		if path.Route.LinkIndex != output.Attrs().Index || path.Neighbor.LinkIndex != output.Attrs().Index ||
			!path.Neighbor.IP.Equal(fixture.gateways[index]) ||
			!bytes.Equal(path.Neighbor.HardwareAddr, fixture.peers[index].Attrs().HardwareAddr) {
			t.Fatalf("resilient candidate differs from its kernel nexthop: %+v", path)
		}
		return
	}
	t.Fatalf("resilient group returned an output outside its members: %+v", path)
}

func TestForwardPathLookupRejectsBlackholeNexthop(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_BLACKHOLE_NEXTHOP_CHILD") {
		return
	}
	fixture := newForwardPathFixture(t, unix.AF_INET, "fb")
	selector := fixture.selector
	selector.Mark = 0
	assertForwardPath(t, fixture, selector, 0)
	loopback, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatalf("inspect blackhole loopback device: %v", err)
	}
	if err := netlink.LinkSetUp(loopback); err != nil {
		t.Fatalf("enable blackhole loopback device: %v", err)
	}
	attributes := mdnetlink.NewAttributeEncoder()
	attributes.Uint32(unix.NHA_ID, forwardPathBlackholeNexthopID)
	attributes.Flag(unix.NHA_BLACKHOLE, true)
	addForwardPathNexthop(t, unix.Nhmsg{Family: unix.AF_INET, Protocol: unix.RTPROT_BGP}, attributes)
	replaceForwardPathNexthopRoute(t, selector, forwardPathBlackholeNexthopID)
	if path, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), selector); usable || path.Link != nil {
		t.Fatalf("blackhole nexthop admitted a forwarding candidate: path=%+v usable=%v err=%v", path, usable, err)
	}
}

func TestForwardPathLookupPreservesContextErrors(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_CONTEXT_CHILD") {
		return
	}
	fixture := newForwardPathFixture(t, unix.AF_INET, "fc")
	assertForwardPath(t, fixture, fixture.selector, 1)
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		path, usable, err := netif.ForwardPathLookup(ctx, slog.Default(), fixture.selector)
		if usable || path.Link != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled lookup lost its context error: path=%+v usable=%v err=%v", path, usable, err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(t.Context(), time.Time{})
		defer cancel()
		path, usable, err := netif.ForwardPathLookup(ctx, slog.Default(), fixture.selector)
		if usable || path.Link != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expired lookup lost its context error: path=%+v usable=%v err=%v", path, usable, err)
		}
	})
}

func TestForwardPathLookupReportsKernelLinkErrors(t *testing.T) {
	if !forwardPathNamespace(t, "MWAN_FORWARD_PATH_KERNEL_ERROR_CHILD") {
		return
	}
	fixture := newForwardPathFixture(t, unix.AF_INET, "fe")
	assertForwardPath(t, fixture, fixture.selector, 1)
	if err := netlink.LinkDel(fixture.input); err != nil {
		t.Fatalf("delete the packet input interface: %v", err)
	}
	path, usable, err := netif.ForwardPathLookup(t.Context(), slog.Default(), fixture.selector)
	var missing netlink.LinkNotFoundError
	if usable || path.Link != nil || !errors.As(err, &missing) || !strings.Contains(err.Error(), "inspect input interface") {
		t.Fatalf("lookup lost the kernel link error or operation: path=%+v usable=%v err=%v", path, usable, err)
	}
}
