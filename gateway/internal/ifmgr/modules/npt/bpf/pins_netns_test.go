//go:build linux && netns

package bpf

import (
	"errors"
	"net/netip"
	"os"
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

func pinTestIngressID(t *testing.T, objects *nptObjects) uint32 {
	t.Helper()
	info, err := objects.NptIngress.Info()
	if err != nil {
		t.Fatal(err)
	}
	identity, available := info.ID()
	if !available {
		t.Fatal("kernel did not report the ingress program identity")
	}
	return uint32(identity)
}

func TestThirdGenerationAdoptsSecondGenerationPins(t *testing.T) {
	link := isolatePinTestNetwork(t)
	pinDirectory := mountPinTestBPFFS(t)
	first, err := newTranslator(pinDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Close() })
	if _, err := first.Reconcile(pinTestPolicies(link)); err != nil {
		t.Fatal(err)
	}
	second, err := newTranslator(pinDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { second.Close() })
	if _, err := second.Reconcile(pinTestPolicies(link)); err != nil {
		t.Fatal(err)
	}
	third, err := newTranslator(pinDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { third.Close() })
	if third.previous == nil {
		t.Fatal("third translator did not open the pinned objects")
	}
	firstID := pinTestIngressID(t, &first.objects)
	secondID := pinTestIngressID(t, &second.objects)
	previousID := pinTestIngressID(t, third.previous)
	if previousID == firstID {
		t.Fatal("third translator opened the first generation's program")
	}
	if previousID != secondID {
		t.Fatalf("third translator opened program %d, want second generation program %d", previousID, secondID)
	}
	if err := third.VerifyUnused([]int{pinTestAbsentIndex}, netip.MustParseAddr("2001:db8:ffff::1"), nil); err != nil {
		t.Fatalf("second generation program was not matched through the pins: %v", err)
	}
	if err := third.VerifyUnused([]int{pinTestAbsentIndex}, netip.MustParseAddr("2001:db8:1::1"), nil); err == nil {
		t.Fatal("edge inside the second generation policy prefix was released")
	}
}

func TestPinFailureDoesNotFailReconcileAndLaterReconcilePins(t *testing.T) {
	link := isolatePinTestNetwork(t)
	pinDirectory := mountPinTestBPFFS(t)
	blockedPin := filepath.Join(pinDirectory, pinIngressName)
	blocker := filepath.Join(blockedPin, "blocker")
	if err := os.MkdirAll(blocker, pinDirectoryMode); err != nil {
		t.Fatal(err)
	}
	translator, err := newTranslator(pinDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { translator.Close() })
	if _, err := translator.Reconcile(pinTestPolicies(link)); err != nil {
		t.Fatalf("pin failure failed Reconcile: %v", err)
	}
	for _, name := range []string{pinEgressName, pinPoliciesName} {
		_, statErr := os.Stat(filepath.Join(pinDirectory, name))
		if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("pin %s exists while the pin path was blocked: %v", name, statErr)
		}
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blockedPin); err != nil {
		t.Fatal(err)
	}
	if _, err := translator.Reconcile(pinTestPolicies(link)); err != nil {
		t.Fatal(err)
	}
	next, err := newTranslator(pinDirectory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { next.Close() })
	if next.previous == nil {
		t.Fatal("pins were not written by the later Reconcile")
	}
	if pinTestIngressID(t, next.previous) != pinTestIngressID(t, &translator.objects) {
		t.Fatal("pins refer to a program other than the translator's current program")
	}
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
