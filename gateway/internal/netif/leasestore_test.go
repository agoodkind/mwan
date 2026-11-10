package netif_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/mwan/internal/netif"
)

func testLeaseRecord(t *testing.T, assignment string) netif.LeaseRecord {
	t.Helper()
	payload, err := json.Marshal(struct {
		Address string `json:"address"`
	}{Address: assignment})
	if err != nil {
		t.Fatal(err)
	}
	return netif.LeaseRecord{
		ConnectionID:     "provider-a",
		Protocol:         netif.LeaseProtocolDHCPv4,
		InterfaceName:    "wan0",
		LinkHardwareAddr: net.HardwareAddr{0x02, 0, 0, 0, 0, 1},
		ProtocolIdentity: []byte{0x01, 0x02, 0x03},
		Clock: netif.LeaseClockSnapshot{
			WallTime: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC),
			BootID:   "boot-a", Uptime: time.Hour,
		},
		Payload: payload,
	}
}

func TestLeaseStoreReplacementAndPermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "leases")
	path := filepath.Join(directory, "provider-a-v4")
	store := netif.NewLeaseStore(path)
	first := testLeaseRecord(t, "192.0.2.10")
	if err := store.Save(first); err != nil {
		t.Fatal(err)
	}
	second := testLeaseRecord(t, "192.0.2.11")
	if err := store.Save(second); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load("provider-a", netif.LeaseProtocolDHCPv4)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loaded.Payload, second.Payload) || loaded.ConnectionID != second.ConnectionID ||
		loaded.Protocol != second.Protocol || loaded.InterfaceName != second.InterfaceName ||
		!bytes.Equal(loaded.LinkHardwareAddr, second.LinkHardwareAddr) ||
		!bytes.Equal(loaded.ProtocolIdentity, second.ProtocolIdentity) || loaded.Clock != second.Clock {
		t.Fatalf("loaded record differs from replacement: %#v", loaded)
	}
	for _, check := range []struct {
		path string
		mode os.FileMode
	}{
		{directory, 0o700},
		{path, 0o600},
	} {
		info, err := os.Stat(check.path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != check.mode {
			t.Errorf("%s mode = %o, want %o", check.path, got, check.mode)
		}
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "provider-a-v4" {
		t.Fatalf("unexpected lease files: %v", entries)
	}
}

func TestLeaseStoreMissingAndDelete(t *testing.T) {
	store := netif.NewLeaseStore(filepath.Join(t.TempDir(), "leases", "provider-a-v4"))
	if _, err := store.Load("provider-a", netif.LeaseProtocolDHCPv4); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing load error = %v", err)
	}
	if err := store.Save(testLeaseRecord(t, "192.0.2.10")); err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("provider-a", netif.LeaseProtocolDHCPv4); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted load error = %v", err)
	}
	if err := store.Delete(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing delete error = %v", err)
	}
}

func TestLeaseStoreRejectsInvalidRecords(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "leases")
	path := filepath.Join(directory, "provider-a-v4")
	store := netif.NewLeaseStore(path)
	valid := testLeaseRecord(t, "192.0.2.10")
	if err := store.Save(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Payload = json.RawMessage(`{"address":`)
	if err := store.Save(invalid); err == nil {
		t.Fatal("invalid payload was saved")
	}
	loaded, err := store.Load("provider-a", netif.LeaseProtocolDHCPv4)
	if err != nil || !bytes.Equal(loaded.Payload, valid.Payload) {
		t.Fatalf("failed replacement changed previous record: %#v, %v", loaded, err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corruptions := map[string][]byte{
		"truncated":       original[:len(original)/2],
		"checksum":        bytes.Replace(original, []byte("192.0.2.10"), []byte("192.0.2.11"), 1),
		"unknown version": bytes.Replace(original, []byte(`"version":1`), []byte(`"version":2`), 1),
		"oversized":       bytes.Repeat([]byte("x"), 1<<20+1),
		"trailing data":   append(bytes.Clone(original), []byte(` {}`)...),
	}
	for name, data := range corruptions {
		t.Run(name, func(t *testing.T) {
			if bytes.Equal(data, original) {
				t.Fatal("corruption did not change the saved record")
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Load("provider-a", netif.LeaseProtocolDHCPv4); err == nil || errors.Is(err, os.ErrNotExist) {
				t.Fatalf("corrupt load error = %v", err)
			} else if name == "checksum" && !strings.Contains(err.Error(), "checksum") {
				t.Fatalf("corrupt payload error = %v, want checksum mismatch", err)
			}
		})
	}
}

func TestLeaseStoreRejectsWrongIdentityAndDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "leases")
	store := netif.NewLeaseStore(filepath.Join(directory, "provider-a-v4"))
	if err := store.Save(testLeaseRecord(t, "192.0.2.10")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("provider-b", netif.LeaseProtocolDHCPv4); err == nil {
		t.Fatal("Load accepted a different connection identity")
	}
	if _, err := store.Load("provider-a", netif.LeaseProtocolDHCPv6); err == nil {
		t.Fatal("Load accepted a different protocol")
	}
	otherDirectory := filepath.Join(t.TempDir(), "leases")
	if err := os.Mkdir(otherDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	otherStore := netif.NewLeaseStore(filepath.Join(otherDirectory, "provider-a-v4"))
	if err := otherStore.Save(testLeaseRecord(t, "192.0.2.10")); err == nil || !strings.Contains(err.Error(), "0700") {
		t.Fatalf("Save accepted permissive directory: %v", err)
	}
}

func TestLeaseStoreRequiresAbsolutePath(t *testing.T) {
	store := netif.NewLeaseStore("relative/lease")
	if err := store.Save(testLeaseRecord(t, "192.0.2.10")); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Save with relative path returned %v", err)
	}
	if _, err := store.Load("provider-a", netif.LeaseProtocolDHCPv4); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Load with relative path returned %v", err)
	}
	if err := store.Delete(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("Delete with relative path returned %v", err)
	}
}

func TestLeaseStoreRequiresBootID(t *testing.T) {
	store := netif.NewLeaseStore(filepath.Join(t.TempDir(), "leases", "provider-a-v4"))
	record := testLeaseRecord(t, "192.0.2.10")
	record.Clock.BootID = ""
	if err := store.Save(record); err == nil || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("Save with empty boot ID returned %v", err)
	}
}

func TestLeaseStoreCreatesOnlyLeafDirectory(t *testing.T) {
	root := t.TempDir()
	missingParent := filepath.Join(root, "missing")
	store := netif.NewLeaseStore(filepath.Join(missingParent, "leases", "record"))
	if err := store.Save(testLeaseRecord(t, "192.0.2.10")); err == nil {
		t.Fatal("Save created a missing parent directory")
	}
	if _, err := os.Lstat(missingParent); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("parent directory state = %v", err)
	}
	if err := os.Mkdir(missingParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testLeaseRecord(t, "192.0.2.10")); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(testLeaseRecord(t, "192.0.2.11")); err != nil {
		t.Fatalf("Save with existing directory failed: %v", err)
	}
	info, err := os.Lstat(filepath.Join(missingParent, "leases"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("lease directory = %v, %v", info, err)
	}
}

func TestLeaseStoreRejectsSymlinkedDirectoryComponents(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	realStore := netif.NewLeaseStore(filepath.Join(realDirectory, "record"))
	if err := realStore.Save(testLeaseRecord(t, "192.0.2.10")); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(realDirectory, link); err != nil {
		t.Fatal(err)
	}
	linkedStore := netif.NewLeaseStore(filepath.Join(link, "record"))
	if err := linkedStore.Save(testLeaseRecord(t, "192.0.2.11")); err == nil {
		t.Fatal("Save accepted a symlinked lease directory")
	}
	if _, err := linkedStore.Load("provider-a", netif.LeaseProtocolDHCPv4); err == nil {
		t.Fatal("Load accepted a symlinked lease directory")
	}
	if err := linkedStore.Delete(); err == nil {
		t.Fatal("Delete accepted a symlinked lease directory")
	}
	if err := os.Mkdir(filepath.Join(realDirectory, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	ancestorStore := netif.NewLeaseStore(filepath.Join(link, "nested", "record"))
	if err := ancestorStore.Save(testLeaseRecord(t, "192.0.2.11")); err == nil {
		t.Fatal("Save accepted a symlinked ancestor")
	}
	loaded, err := realStore.Load("provider-a", netif.LeaseProtocolDHCPv4)
	if err != nil || !bytes.Contains(loaded.Payload, []byte("192.0.2.10")) {
		t.Fatalf("redirected record changed: %v, %v", loaded.Payload, err)
	}
}
