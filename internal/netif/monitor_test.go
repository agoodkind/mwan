package netif

import (
	"net"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

func newTestMonitor(iface string, ifIndex int) *Monitor {
	return &Monitor{
		cfg:     MonitorConfig{Iface: iface},
		ifIndex: ifIndex,
	}
}

func TestAddrUpdateToEventAdd(t *testing.T) {
	m := newTestMonitor("eth0", 7)
	ip := net.ParseIP("2001:db8::1")
	upd := netlink.AddrUpdate{
		LinkIndex:   7,
		NewAddr:     true,
		LinkAddress: net.IPNet{IP: ip, Mask: net.CIDRMask(64, 128)},
	}
	got := m.addrUpdateToEvent(upd)
	if got.Kind != EvAddrAdded {
		t.Fatalf("kind got %s, want %s", got.Kind, EvAddrAdded)
	}
	if got.Family != "inet6" {
		t.Errorf("family got %q want inet6", got.Family)
	}
	if got.CIDR != "2001:db8::1/64" {
		t.Errorf("cidr got %q want 2001:db8::1/64", got.CIDR)
	}
}

func TestAddrUpdateToEventOtherIfaceFiltered(t *testing.T) {
	m := newTestMonitor("eth0", 7)
	upd := netlink.AddrUpdate{
		LinkIndex:   99,
		NewAddr:     true,
		LinkAddress: net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)},
	}
	if got := m.addrUpdateToEvent(upd); got.Kind != EvUnknown {
		t.Fatalf("expected EvUnknown for foreign iface, got %s", got.Kind)
	}
}

func TestRouteUpdateToEventDefaultV4(t *testing.T) {
	m := newTestMonitor("eth0", 7)
	upd := netlink.RouteUpdate{
		Type: unix.RTM_NEWROUTE,
		Route: netlink.Route{
			LinkIndex: 7,
			Family:    unix.AF_INET,
			Gw:        net.ParseIP("192.0.2.1"),
			// Dst nil = default
		},
	}
	got := m.routeUpdateToEvent(upd)
	if got.Kind != EvRouteAdded {
		t.Fatalf("kind got %s, want %s", got.Kind, EvRouteAdded)
	}
	if got.Family != "inet" {
		t.Errorf("family got %q want inet", got.Family)
	}
	if got.Via != "192.0.2.1" {
		t.Errorf("via got %q want 192.0.2.1", got.Via)
	}
}

func TestRouteUpdateToEventNonDefaultIgnored(t *testing.T) {
	m := newTestMonitor("eth0", 7)
	_, dst, _ := net.ParseCIDR("192.0.2.0/24")
	upd := netlink.RouteUpdate{
		Type: unix.RTM_NEWROUTE,
		Route: netlink.Route{
			LinkIndex: 7,
			Family:    unix.AF_INET,
			Dst:       dst,
		},
	}
	if got := m.routeUpdateToEvent(upd); got.Kind != EvUnknown {
		t.Fatalf("expected EvUnknown for non-default, got %s", got.Kind)
	}
}

func TestRouteUpdateDeleteV6(t *testing.T) {
	m := newTestMonitor("eth0", 7)
	upd := netlink.RouteUpdate{
		Type: unix.RTM_DELROUTE,
		Route: netlink.Route{
			LinkIndex: 7,
			Family:    unix.AF_INET6,
			Gw:        net.ParseIP("fe80::1"),
		},
	}
	got := m.routeUpdateToEvent(upd)
	if got.Kind != EvRouteDeleted {
		t.Fatalf("kind got %s want %s", got.Kind, EvRouteDeleted)
	}
	if got.Family != "inet6" {
		t.Errorf("family got %q want inet6", got.Family)
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
