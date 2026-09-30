package netif

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func recoveryTestLink(t *testing.T) *net.Interface {
	t.Helper()
	links, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for i := range links {
		if len(links[i].HardwareAddr) != 0 {
			return &links[i]
		}
	}
	t.Skip("no interface with a hardware address")
	return nil
}

func TestLeaseRecoveryStoreRoundTripAndIsolation(t *testing.T) {
	link := recoveryTestLink(t)
	directory := filepath.Join(t.TempDir(), "leases")
	store, err := NewLeaseRecoveryStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	v4 := LeaseInfo{
		State: LeaseBound, LinkHardwareAddr: link.HardwareAddr,
		IP: net.IPv4(192, 0, 2, 8), PrefixLen: 24, Gateway: net.IPv4(192, 0, 2, 1),
		Server: net.IPv4(192, 0, 2, 1), LeaseTime: time.Hour,
		AcquiredAt: now, RenewAt: now.Add(30 * time.Minute),
		RebindAt: now.Add(45 * time.Minute), ExpiresAt: now.Add(time.Hour),
	}
	clientID := []byte{0xff, 1, 2, 3}
	if err := store.SaveDHCPv4("provider/a", link.Name, clientID, v4); err != nil {
		t.Fatal(err)
	}
	duid := []byte{0, 3, 0, 1, 2, 0, 0, 0, 0, 1}
	v6 := DHCPv6PDLease{
		LinkName: link.Name, LinkIndex: link.Index,
		LinkHardwareAddr: link.HardwareAddr, DUID: duid, IAID: 7,
		ServerID: []byte{0, 3, 0, 1, 2, 0, 0, 0, 0, 2}, AcquiredAt: now,
		RenewAt: now.Add(30 * time.Minute), RebindAt: now.Add(45 * time.Minute),
		RequestPrefix: true, Prefixes: []DelegatedPrefix{{
			Prefix:         netip.MustParsePrefix("2001:db8:1::/56"),
			PreferredUntil: now.Add(50 * time.Minute), ValidUntil: now.Add(time.Hour),
		}},
	}
	if err := store.SaveDHCPv6("provider/a", v6); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Name() == entries[1].Name() {
		t.Fatalf("saved files = %v", entries)
	}
	loaded4, err := store.LoadDHCPv4("provider/a", link, clientID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded4.IP.Equal(v4.IP) || loaded4.LinkIndex != link.Index || loaded4.Err != nil || loaded4.InvalidationEpoch != 0 || !loaded4.ExpiresAt.After(time.Now().Add(59*time.Minute)) {
		t.Fatalf("loaded DHCPv4 assignment = %+v", loaded4)
	}
	loaded6, err := store.LoadDHCPv6("provider/a", link, DHCPv6PDConfig{
		Iface: link.Name, DUID: duid, IAID: 7, RequestPrefix: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if loaded6.LinkIndex != link.Index || len(loaded6.Prefixes) != 1 || loaded6.Prefixes[0].Prefix != v6.Prefixes[0].Prefix {
		t.Fatalf("loaded DHCPv6 assignment = %+v", loaded6)
	}
	if err := store.Delete("provider/a", LeaseProtocolDHCPv4); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDHCPv4("provider/a", link, clientID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted DHCPv4 load = %v", err)
	}
	if _, err := store.LoadDHCPv6("provider/a", link, DHCPv6PDConfig{Iface: link.Name, DUID: duid, IAID: 7, RequestPrefix: true}); err != nil {
		t.Fatalf("DHCPv4 deletion changed DHCPv6: %v", err)
	}
}

func TestLeaseRecoveryStoreRejectsIdentityAndCorruption(t *testing.T) {
	link := recoveryTestLink(t)
	directory := filepath.Join(t.TempDir(), "leases")
	store, err := NewLeaseRecoveryStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	lease := LeaseInfo{
		State: LeaseBound, LinkHardwareAddr: link.HardwareAddr,
		IP: net.IPv4(192, 0, 2, 8), PrefixLen: 24, LeaseTime: time.Hour,
		AcquiredAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.SaveDHCPv4("provider", link.Name, []byte{1, 2}, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDHCPv4("provider", link, []byte{1, 3}); err == nil {
		t.Fatal("accepted a different client ID")
	}
	changed := *link
	changed.HardwareAddr = append(net.HardwareAddr(nil), link.HardwareAddr...)
	changed.HardwareAddr[0] ^= 1
	if _, err := store.LoadDHCPv4("provider", &changed, []byte{1, 2}); err == nil {
		t.Fatal("accepted a different hardware address")
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, entries[0].Name())
	if err := os.WriteFile(path, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDHCPv4("provider", link, []byte{1, 2}); err == nil {
		t.Fatal("accepted a corrupt record")
	}
}

func TestLeaseRecoveryClockElapsed(t *testing.T) {
	start := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	saved := LeaseClockSnapshot{WallTime: start, BootID: "boot-a", Uptime: time.Hour}
	current := LeaseClockSnapshot{WallTime: start.Add(-time.Hour), BootID: "boot-a", Uptime: time.Hour + time.Minute}
	elapsed, err := leaseElapsed(saved, current)
	if err != nil || elapsed != time.Minute {
		t.Fatalf("same boot elapsed = %s, %v", elapsed, err)
	}
	deadline := rebaseLeaseDeadline(start.Add(10*time.Minute), saved, current, elapsed)
	if got := deadline.Sub(current.WallTime); got != 9*time.Minute {
		t.Fatalf("remaining validity = %s", got)
	}
	current.Uptime = saved.Uptime - 1
	if _, err := leaseElapsed(saved, current); err == nil {
		t.Fatal("accepted backward uptime")
	}
	current.BootID = "boot-b"
	if _, err := leaseElapsed(saved, current); err == nil {
		t.Fatal("accepted backward wall time after reboot")
	}
}

func savePruneTestRecord(t *testing.T, store *LeaseRecoveryStore, link *net.Interface, connectionID string, protocol LeaseProtocol) string {
	t.Helper()
	before, err := os.ReadDir(store.directory)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
	}
	previous := make(map[string]bool, len(before))
	for _, entry := range before {
		previous[entry.Name()] = true
	}
	now := time.Now()
	switch protocol {
	case LeaseProtocolDHCPv4:
		lease := LeaseInfo{
			State: LeaseBound, LinkHardwareAddr: link.HardwareAddr,
			IP: net.IPv4(192, 0, 2, 8), PrefixLen: 24, LeaseTime: time.Hour,
			AcquiredAt: now, ExpiresAt: now.Add(time.Hour),
		}
		err = store.SaveDHCPv4(connectionID, link.Name, []byte{1, 2, 3}, lease)
	case LeaseProtocolDHCPv6:
		lease := DHCPv6PDLease{
			LinkName: link.Name, LinkHardwareAddr: link.HardwareAddr,
			DUID: []byte{0, 3, 0, 1, 2, 0, 0, 0, 0, 1}, IAID: 7,
			ServerID:   []byte{0, 3, 0, 1, 2, 0, 0, 0, 0, 2},
			AcquiredAt: now, RenewAt: now.Add(30 * time.Minute), RebindAt: now.Add(45 * time.Minute),
			RequestPrefix: true, Prefixes: []DelegatedPrefix{{
				Prefix:         netip.MustParsePrefix("2001:db8:1::/56"),
				PreferredUntil: now.Add(50 * time.Minute), ValidUntil: now.Add(time.Hour),
			}},
		}
		err = store.SaveDHCPv6(connectionID, lease)
	default:
		t.Fatalf("unsupported protocol %s", protocol)
	}
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadDir(store.directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range after {
		if !previous[entry.Name()] {
			return filepath.Join(store.directory, entry.Name())
		}
	}
	t.Fatal("saved lease record was not created")
	return ""
}

func TestLeaseRecoveryPruneUnconfigured(t *testing.T) {
	link := recoveryTestLink(t)
	root := t.TempDir()
	wan, err := NewLeaseRecoveryStore(filepath.Join(root, "wan"))
	if err != nil {
		t.Fatal(err)
	}
	roles, err := NewLeaseRecoveryStore(filepath.Join(root, "roles"))
	if err != nil {
		t.Fatal(err)
	}
	active4 := savePruneTestRecord(t, wan, link, "provider-a", LeaseProtocolDHCPv4)
	active6 := savePruneTestRecord(t, wan, link, "provider-a", LeaseProtocolDHCPv6)
	removed4 := savePruneTestRecord(t, wan, link, "provider-b", LeaseProtocolDHCPv4)
	removed6 := savePruneTestRecord(t, wan, link, "provider-b", LeaseProtocolDHCPv6)
	role := savePruneTestRecord(t, roles, link, "provider-b", LeaseProtocolDHCPv4)
	if err := wan.PruneUnconfigured(map[string]bool{"provider-a": true}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{active4, active6, role} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("retained record %s: %v", path, err)
		}
	}
	for _, path := range []string{removed4, removed6} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed record %s: %v", path, err)
		}
	}
	if present, err := wan.Has("provider-a", LeaseProtocolDHCPv4); err != nil || !present {
		t.Fatalf("active record present=%t, err=%v", present, err)
	}
	if present, err := wan.Has("provider-b", LeaseProtocolDHCPv4); err != nil || present {
		t.Fatalf("removed record present=%t, err=%v", present, err)
	}
}

func TestLeaseRecoveryPruneRetainsMalformedFiles(t *testing.T) {
	link := recoveryTestLink(t)
	store, err := NewLeaseRecoveryStore(filepath.Join(t.TempDir(), "wan"))
	if err != nil {
		t.Fatal(err)
	}
	removed := savePruneTestRecord(t, store, link, "removed", LeaseProtocolDHCPv4)
	corrupt := savePruneTestRecord(t, store, link, "corrupt", LeaseProtocolDHCPv4)
	if err := os.WriteFile(corrupt, []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(store.directory, "unknown.txt")
	if err := os.WriteFile(unknown, []byte("leave alone"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := savePruneTestRecord(t, store, link, "symlink", LeaseProtocolDHCPv4)
	if err := os.Remove(symlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unknown, symlink); err != nil {
		t.Fatal(err)
	}
	misplaced := savePruneTestRecord(t, store, link, "misplaced", LeaseProtocolDHCPv4)
	other := savePruneTestRecord(t, store, link, "other", LeaseProtocolDHCPv4)
	if err := os.Remove(other); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(misplaced, other); err != nil {
		t.Fatal(err)
	}
	if err := store.PruneUnconfigured(nil); err == nil {
		t.Fatal("malformed files did not produce an error")
	}
	if _, err := os.Stat(removed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("valid removed record: %v", err)
	}
	for _, path := range []string{corrupt, unknown, symlink, other} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("malformed file %s was removed: %v", path, err)
		}
	}
	if present, err := store.Has("corrupt", LeaseProtocolDHCPv4); err == nil || present {
		t.Fatalf("corrupt record present=%t, err=%v", present, err)
	}
}
