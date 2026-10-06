//go:build linux && netns

package bpf

import (
	"net/netip"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

const (
	pinTestAbsentIndex = 1 << 20
	pinTestLinkName    = "npt0"
)

func isolatePinTestNetwork(t *testing.T) netlink.Link {
	t.Helper()
	runtime.LockOSThread()
	previous, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	current, err := netns.New()
	if err != nil {
		previous.Close()
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := netns.Set(previous); err != nil {
			t.Error(err)
		}
		current.Close()
		previous.Close()
		runtime.UnlockOSThread()
	})
	link := &netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: pinTestLinkName}}
	if err := netlink.LinkAdd(link); err != nil {
		t.Fatal(err)
	}
	created, err := netlink.LinkByName(pinTestLinkName)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func mountPinTestBPFFS(t *testing.T) string {
	t.Helper()
	mountpoint := t.TempDir()
	if err := unix.Mount("bpf", mountpoint, "bpf", 0, ""); err != nil {
		t.Fatalf("mount bpffs: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(mountpoint, 0); err != nil {
			t.Error(err)
		}
	})
	return filepath.Join(mountpoint, "mwan", "npt")
}

func pinTestPolicies(link netlink.Link) []InterfacePolicy {
	pair := PrefixPair{ID: 1, Internal: netip.MustParsePrefix("fd00:1::/48"), External: netip.MustParsePrefix("2001:db8:1::/48")}
	return []InterfacePolicy{{IfIndex: link.Attrs().Index, Pairs: []PrefixPair{pair}}}
}

func TestEdgeReleaseBlocksOnUnmatchedOwnedFilter(t *testing.T) {
	link := isolatePinTestNetwork(t)
	pinDirectory := mountPinTestBPFFS(t)
	unusedEdge := netip.MustParseAddr("2001:db8:ffff::1")
	stale, err := newTranslator(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.Reconcile(pinTestPolicies(link)); err != nil {
		t.Fatal(err)
	}
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := newTranslator(pinDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { current.Close() })
	if err := current.VerifyUnused([]int{pinTestAbsentIndex}, unusedEdge, nil); err == nil {
		t.Fatal("edge release passed while an owned filter matched no known program")
	}
	if err := removeFilters(link); err != nil {
		t.Fatal(err)
	}
	if err := current.VerifyUnused([]int{pinTestAbsentIndex}, unusedEdge, nil); err != nil {
		t.Fatalf("edge release failed after the stale filter was removed: %v", err)
	}
}

func TestEdgeReleaseMatchesPreviousProgramThroughPins(t *testing.T) {
	link := isolatePinTestNetwork(t)
	pinDirectory := mountPinTestBPFFS(t)
	first, err := newTranslator(pinDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Reconcile(pinTestPolicies(link)); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := newTranslator(pinDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	if err := second.VerifyUnused([]int{pinTestAbsentIndex}, netip.MustParseAddr("2001:db8:ffff::1"), nil); err != nil {
		t.Fatalf("previous program was not matched through the pins: %v", err)
	}
	if err := second.VerifyUnused([]int{pinTestAbsentIndex}, netip.MustParseAddr("2001:db8:1::1"), nil); err == nil {
		t.Fatal("edge inside the previous policy prefix was released")
	}
}
