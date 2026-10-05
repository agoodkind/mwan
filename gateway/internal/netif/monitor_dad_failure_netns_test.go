//go:build linux && netns

package netif

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const dadFailureChildEnv = "MWAN_DAD_FAILURE_TEST_CHILD"

func TestMonitorReportsDADFailedAddress(t *testing.T) {
	if os.Getenv(dadFailureChildEnv) == "1" {
		runMonitorDADFailureChild(t)
		return
	}
	if os.Geteuid() != 0 {
		t.Skip("network namespace requires root")
	}
	child := exec.Command(os.Args[0], "-test.run=^TestMonitorReportsDADFailedAddress$")
	child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	child.Env = append(os.Environ(), dadFailureChildEnv+"=1")
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("isolated DAD failure test: %v: %s", err, output)
	}
}

func runMonitorDADFailureChild(t *testing.T) {
	const clientName = "dad-client"
	const routerName = "dad-router"
	const cidr = "2001:db8:523::d/128"
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: clientName, HardwareAddr: net.HardwareAddr{0x02, 0, 0x5e, 0, 0x05, 0x24}},
		PeerName:  routerName,
	}); err != nil {
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
	if err := os.WriteFile("/proc/sys/net/ipv6/conf/"+clientName+"/dad_transmits", []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(router); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(client); err != nil {
		t.Fatal(err)
	}
	waitRALinkLocal(t, router)
	waitRALinkLocal(t, client)

	routerAddress, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	routerAddress.Flags = IFAFNoDAD
	if err := netlink.AddrAdd(router, routerAddress); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	monitor := NewMonitor(ctx, logger, MonitorConfig{Iface: clientName})
	waitObservedSnapshot(t, monitor.Events, func(snapshot *Snapshot) bool {
		return snapshot.IfIndex == client.Attrs().Index
	})
	clientAddress, err := netlink.ParseAddr(cidr)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(client, clientAddress); err != nil {
		t.Fatal(err)
	}

	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var kernelFailed, eventFailed bool
	for !kernelFailed || !eventFailed {
		select {
		case event := <-monitor.Events:
			if event.Kind != EvAddrAdded || event.CIDR != cidr {
				continue
			}
			if event.Family != "inet6" || event.IfIndex != client.Attrs().Index {
				t.Fatalf("DAD address identity = %+v", event)
			}
			if event.Flags&IFAFDADFailed != 0 {
				if event.Flags&IFAFTentative == 0 {
					t.Fatalf("failed DAD address appeared usable in event: %+v", event)
				}
				eventFailed = true
			}
		case <-tick.C:
			address, exists := raKernelAddress(t, client, cidr)
			if exists && address.Flags&IFAFDADFailed != 0 {
				if address.Flags&IFAFTentative == 0 {
					t.Fatalf("failed DAD address appeared usable in kernel: %+v", address)
				}
				kernelFailed = true
			}
		case <-deadline.C:
			t.Fatalf("DAD failure not observed: kernel=%t monitor_event=%t", kernelFailed, eventFailed)
		}
	}
	snapshot := raFreshSnapshot(t, logger, clientName)
	address := raSnapshotAddress(t, snapshot, cidr)
	if address.Flags&(IFAFDADFailed|IFAFTentative) != IFAFDADFailed|IFAFTentative {
		t.Fatalf("failed DAD address appeared usable in snapshot: %+v", address)
	}
}
