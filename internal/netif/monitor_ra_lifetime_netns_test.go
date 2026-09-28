//go:build linux && netns

package netif

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/ndp"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const raLifetimeChildEnv = "MWAN_RA_LIFETIME_TEST_CHILD"

func TestMonitorObservesRALifetimes(t *testing.T) {
	if os.Getenv(raLifetimeChildEnv) == "1" {
		runMonitorRALifetimeChild(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespace requires root")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestMonitorObservesRALifetimes$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), raLifetimeChildEnv+"=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated RA lifetime test: %v: %s", err, output)
	}
}

func runMonitorRALifetimeChild(t *testing.T) {
	const clientName = "ra-life-client"
	const routerName = "ra-life-router"
	clientVeth := &netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: clientName, HardwareAddr: net.HardwareAddr{0x02, 0, 0x5e, 0, 0x05, 0x23}},
		PeerName:  routerName,
	}
	if err := netlink.LinkAdd(clientVeth); err != nil {
		t.Fatal(err)
	}
	client, err := netlink.LinkByName(clientName)
	if err != nil {
		t.Fatal(err)
	}
	router, err := netlink.LinkByName(routerName)
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{
		"accept_ra": "2", "autoconf": "1", "use_tempaddr": "0",
		"addr_gen_mode": "0", "dad_transmits": "1",
	} {
		path := fmt.Sprintf("/proc/sys/net/ipv6/conf/%s/%s", clientName, key)
		if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := netlink.LinkSetUp(router); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(client); err != nil {
		t.Fatal(err)
	}
	waitRALinkLocal(t, router)
	waitRALinkLocal(t, client)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: clientName})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == client.Attrs().Index
	})
	routerInterface, err := net.InterfaceByName(routerName)
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := ndp.Listen(routerInterface, ndp.LinkLocal)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	ra := &ndp.RouterAdvertisement{
		CurrentHopLimit: 64,
		Options: []ndp.Option{&ndp.PrefixInformation{
			PrefixLength: 64, Prefix: netip.MustParseAddr("2001:db8:523::"),
			OnLink: true, AutonomousAddressConfiguration: true,
			PreferredLifetime: 4 * time.Second, ValidLifetime: 10 * time.Second,
		}},
	}
	if err := conn.WriteTo(ra, nil, netip.MustParseAddr("ff02::1")); err != nil {
		t.Fatal(err)
	}

	deadline := time.NewTimer(13 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	var addressCIDR string
	var checkedTentative, sawUsable, checkedUsable, sawDeprecated, sawDeleted bool
	for !sawDeleted {
		select {
		case event := <-monitor.Events:
			if event.Family != "inet6" || !strings.HasPrefix(event.CIDR, "2001:db8:523:") {
				continue
			}
			if event.IfIndex != client.Attrs().Index {
				t.Fatalf("RA address event used wrong interface: %+v", event)
			}
			if addressCIDR == "" {
				addressCIDR = event.CIDR
			} else if event.CIDR != addressCIDR {
				t.Fatalf("RA address changed from %s to %s", addressCIDR, event.CIDR)
			}
			if event.Kind == EvAddrDeleted {
				sawDeleted = true
				continue
			}
			if event.Kind != EvAddrAdded {
				continue
			}
			if event.Flags&IFAFTentative == 0 && event.Flags&IFAFDeprecated == 0 &&
				event.PreferredLifetime > 0 && event.ValidLifetime > event.PreferredLifetime {
				sawUsable = true
			}
		case <-tick.C:
			if !checkedTentative {
				addresses, err := netlink.AddrList(client, netlink.FAMILY_V6)
				if err != nil {
					t.Fatal(err)
				}
				for _, address := range addresses {
					if strings.HasPrefix(address.IPNet.String(), "2001:db8:523:") &&
						address.Flags&IFAFTentative != 0 {
						addressCIDR = address.IPNet.String()
						snapshot := raFreshSnapshot(t, logger, clientName)
						observed := raSnapshotAddress(t, snapshot, addressCIDR)
						if observed.Flags&IFAFTentative == 0 || observed.ValidLifetime <= 0 {
							t.Fatalf("tentative snapshot address = %+v", observed)
						}
						checkedTentative = true
					}
				}
			}
			if addressCIDR == "" {
				continue
			}
			kernelAddress, exists := raKernelAddress(t, client, addressCIDR)
			if !exists || !sawUsable {
				continue
			}
			if kernelAddress.Flags&IFAFDeprecated != 0 && !sawDeprecated {
				sawDeprecated = true
				snapshot := raFreshSnapshot(t, logger, clientName)
				address := raSnapshotAddress(t, snapshot, addressCIDR)
				if address.Flags&IFAFDeprecated == 0 || address.PreferredLifetime != 0 ||
					address.ValidLifetime <= 0 {
					t.Fatalf("deprecated snapshot address = %+v", address)
				}
			}
		case <-deadline.C:
			t.Fatalf("RA lifecycle timed out: tentative_snapshot=%t usable=%t deprecated=%t deleted=%t cidr=%s",
				checkedTentative, sawUsable, sawDeprecated, sawDeleted, addressCIDR)
		}
		if sawUsable && !checkedUsable && !sawDeprecated {
			snapshot := raFreshSnapshot(t, logger, clientName)
			address := raSnapshotAddress(t, snapshot, addressCIDR)
			if address.Flags&(IFAFTentative|IFAFDeprecated) != 0 ||
				address.PreferredLifetime <= 0 || address.ValidLifetime <= address.PreferredLifetime {
				t.Fatalf("usable snapshot address = %+v", address)
			}
			kernelAddress, exists := raKernelAddress(t, client, addressCIDR)
			if !exists || kernelAddress.Flags&(IFAFTentative|IFAFDeprecated) != 0 {
				t.Fatalf("usable kernel address = %+v, present=%t", kernelAddress, exists)
			}
			checkedUsable = true
		}
	}
	if !checkedTentative || !sawUsable || !checkedUsable || !sawDeprecated {
		t.Fatalf("incomplete RA lifecycle: tentative_snapshot=%t usable=%t usable_snapshot=%t deprecated=%t", checkedTentative, sawUsable, checkedUsable, sawDeprecated)
	}
	if _, exists := raKernelAddress(t, client, addressCIDR); exists {
		t.Fatal("expired RA address remains in kernel")
	}
	snapshot := raFreshSnapshot(t, logger, clientName)
	for _, address := range snapshot.Addresses {
		if address.CIDR == addressCIDR {
			t.Fatalf("expired RA address remains in monitor snapshot: %+v", address)
		}
	}
}

func waitRALinkLocal(t *testing.T, link netlink.Link) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
		if err != nil {
			t.Fatal(err)
		}
		for _, address := range addresses {
			if address.IP.IsLinkLocalUnicast() && address.Flags&IFAFTentative == 0 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("link-local DAD did not finish on %s", link.Attrs().Name)
}

func raKernelAddress(t *testing.T, link netlink.Link, cidr string) (netlink.Addr, bool) {
	t.Helper()
	addresses, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if address.IPNet.String() == cidr {
			return address, true
		}
	}
	return netlink.Addr{}, false
}

func raFreshSnapshot(t *testing.T, logger *slog.Logger, iface string) *Snapshot {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: iface})
	return waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex != 0
	})
}

func raSnapshotAddress(t *testing.T, snapshot *Snapshot, cidr string) CurrentAddr {
	t.Helper()
	for _, address := range snapshot.Addresses {
		if address.CIDR == cidr {
			return address
		}
	}
	t.Fatalf("RA address %s missing from monitor snapshot", cidr)
	return CurrentAddr{}
}
