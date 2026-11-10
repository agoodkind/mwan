//go:build linux && netns

package netif

import (
	"context"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/interfaceintent"
)

func TestOwnedRecoveryRetainsOnlyValidAssociations(t *testing.T) {
	const childEnv = "MWAN_OWNED_RECOVERY_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^TestOwnedRecoveryRetainsOnlyValidAssociations$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated owned recovery test: %v: %s", err, output)
		}
		return
	}

	const linkName = "owned-recover0"
	if err := netlink.LinkAdd(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: linkName, Alias: "old"}}); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(linkName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	connection := interfaceintent.Connection{ID: "owned-recovery-test", Name: linkName}
	ready := OwnedLinkResult{ConnectionID: connection.ID.String(), Name: linkName, ActualName: linkName, IfIndex: link.Attrs().Index, Status: OwnedLinkReady}
	statePath := filepath.Join(t.TempDir(), "owned-recovery.json")
	reconciler, err := NewOwnedStaticReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	iana := netip.MustParsePrefix("2001:db8:2::100/128")
	npt := netip.MustParsePrefix("2001:db8:30::1/128")
	oldStatic := netip.MustParsePrefix("2001:db8:2::10/128")
	newStatic := netip.MustParsePrefix("2001:db8:2::11/128")
	initial := interfaceintent.Family{Addresses: []interfaceintent.Address{{Prefix: iana}, {Prefix: npt}, {Prefix: oldStatic}}}
	if err := reconciler.ReconcileFamilyRoutes(context.Background(), connection, "ipv6", initial, nil, ready); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewOwnedStaticReconciler(statePath)
	if err != nil {
		t.Fatal(err)
	}
	current := interfaceintent.Family{Addresses: []interfaceintent.Address{{Prefix: newStatic}}}
	now := time.Now()
	pdValidUntil := now.Add(150 * time.Millisecond)
	ianaValidUntil := now.Add(5 * time.Second)
	retention := RecordedRetention{All: false, Prefixes: map[netip.Prefix]bool{iana: now.Before(ianaValidUntil), npt: now.Before(pdValidUntil)}}
	if err := restarted.ReconcileFamilyRoutesWithLifetimesRetaining(context.Background(), connection, "ipv6", current, nil, ready, nil, retention); err != nil {
		t.Fatal(err)
	}
	assertOwnedRecoveryAddress(t, link, iana, true)
	assertOwnedRecoveryAddress(t, link, npt, true)
	assertOwnedRecoveryAddress(t, link, oldStatic, false)
	assertOwnedRecoveryAddress(t, link, newStatic, true)
	time.Sleep(max(pdValidUntil.Sub(time.Now()), 0) + 10*time.Millisecond)
	now = time.Now()
	if !now.Before(ianaValidUntil) || now.Before(pdValidUntil) {
		t.Fatalf("original association deadlines did not differ: IA_NA=%s PD=%s now=%s", ianaValidUntil, pdValidUntil, now)
	}
	retention = RecordedRetention{All: false, Prefixes: map[netip.Prefix]bool{iana: now.Before(ianaValidUntil)}}
	if err := restarted.ReconcileFamilyRoutesWithLifetimesRetaining(context.Background(), connection, "ipv6", current, nil, ready, nil, retention); err != nil {
		t.Fatal(err)
	}
	assertOwnedRecoveryAddress(t, link, iana, true)
	assertOwnedRecoveryAddress(t, link, npt, false)

	if err := netlink.LinkSetAlias(link, "new"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.ReconcileFamilyRoutesWithLifetimesRetaining(context.Background(), connection, "ipv6", interfaceintent.Family{}, nil, ready, nil, retention); err != nil {
		t.Fatal(err)
	}
	if len(restarted.journal.Objects) != 0 {
		t.Fatalf("old link identity retained journal entries: %+v", restarted.journal.Objects)
	}
}

func assertOwnedRecoveryAddress(t *testing.T, link netlink.Link, prefix netip.Prefix, want bool) {
	t.Helper()
	addresses, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if address.IPNet.String() == prefix.String() {
			if !want {
				t.Fatalf("address %s remains installed", prefix)
			}
			return
		}
	}
	if want {
		t.Fatalf("address %s was removed", prefix)
	}
}
