package netif

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
)

func TestAddrUpdateToEventOtherIfaceFiltered(t *testing.T) {
	m := &Monitor{cfg: MonitorConfig{Iface: "eth0"}}
	upd := netlink.AddrUpdate{
		LinkIndex:   99,
		NewAddr:     true,
		LinkAddress: net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)},
	}
	if got := m.addrUpdateToEvent(upd); got.Kind != EvUnknown {
		t.Fatalf("expected EvUnknown for foreign iface, got %s", got.Kind)
	}
}

func TestEventKindString(t *testing.T) {
	cases := map[EventKind]string{
		EvUnknown:      "unknown",
		EvRouteAdded:   "route-added",
		EvRouteDeleted: "route-deleted",
		EvAddrAdded:    "addr-added",
		EvAddrDeleted:  "addr-deleted",
		EvLinkUp:       "link-up",
		EvLinkDown:     "link-down",
	}
	for k, want := range cases {
		if got := k.String(); got != want {
			t.Errorf("EventKind(%d).String() = %q, want %q", k, got, want)
		}
	}
}
