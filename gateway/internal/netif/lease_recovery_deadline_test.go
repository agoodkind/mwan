package netif

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestLeaseRecoveryDoesNotExtendSavedDeadline(t *testing.T) {
	link := recoveryTestLink(t)
	store, err := NewLeaseRecoveryStore(filepath.Join(t.TempDir(), "leases"))
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 40; attempt++ {
		now := time.Now()
		lease := LeaseInfo{
			State: LeaseBound, LinkHardwareAddr: link.HardwareAddr,
			IP: net.IPv4(192, 0, 2, 8), PrefixLen: 24, LeaseTime: time.Hour,
			AcquiredAt: now, ExpiresAt: now.Add(time.Hour),
		}
		if err := store.SaveDHCPv4("provider", link.Name, nil, lease); err != nil {
			t.Fatal(err)
		}
		loaded, err := store.LoadDHCPv4("provider", link, nil)
		if err != nil {
			t.Fatal(err)
		}
		if extension := loaded.ExpiresAt.Sub(lease.ExpiresAt); extension > 0 {
			t.Fatalf("saved deadline extended by %s on attempt %d", extension, attempt)
		}
	}
}
