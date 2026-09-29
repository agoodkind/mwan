package netif

import (
	"net/netip"
	"testing"
	"time"
)

func TestDeclineAddressWaitsForQueueCapacity(t *testing.T) {
	address := netip.MustParseAddr("2001:db8::4")
	var client DHCPv6PDClient
	client.lease.Addresses = []DelegatedAddress{{Address: address, PreferredUntil: time.Time{}, ValidUntil: time.Time{}}}
	client.declines = make(chan netip.Addr, 1)
	client.pending = make(map[netip.Addr]bool)
	client.declines <- netip.MustParseAddr("2001:db8::5")
	result := make(chan bool, 1)
	go func() {
		result <- client.DeclineAddress(address)
	}()
	select {
	case accepted := <-result:
		t.Fatalf("full queue returned before capacity was available: accepted=%t", accepted)
	case <-time.After(20 * time.Millisecond):
	}
	snapshot := make(chan DHCPv6PDLease, 1)
	go func() {
		snapshot <- client.LastLease()
	}()
	select {
	case lease := <-snapshot:
		if len(lease.Addresses) != 1 || lease.Addresses[0].Address != address {
			t.Fatalf("client lease changed while decline waited: %+v", lease)
		}
	case <-time.After(time.Second):
		t.Fatal("client lease read blocked behind the full decline queue")
	}
	<-client.declines
	select {
	case accepted := <-result:
		if !accepted {
			t.Fatal("decline was rejected after queue capacity became available")
		}
	case <-time.After(time.Second):
		t.Fatal("decline did not queue after capacity became available")
	}
}
