//go:build linux && netns

package slaachealth_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/mdlayher/ndp"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/slaachealth"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
)

func TestProlongedDegradationKeepsAddressAndSolicitsRouter(t *testing.T) {
	const childEnvironment = "MWAN_SLAAC_RECOVERY_CHILD"
	if os.Getenv(childEnvironment) != "1" {
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnvironment+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("run isolated SLAAC recovery: %v: %s", err, output)
		}
		return
	}
	const iface = "slaac-c0"
	veth := &netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: iface}, PeerName: "slaac-r0"}
	if err := netlink.LinkAdd(veth); err != nil {
		t.Fatal(err)
	}
	router, err := netlink.LinkByName("slaac-r0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		"/proc/sys/net/ipv6/conf/"+iface+"/router_solicitations",
		[]byte("0"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(veth); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(router); err != nil {
		t.Fatal(err)
	}
	client, err := netlink.LinkByName(iface)
	if err != nil {
		t.Fatal(err)
	}
	address, err := netlink.ParseAddr("2001:db8:1::10/128")
	if err != nil {
		t.Fatal(err)
	}
	address.Flags = unix.IFA_F_NODAD
	address.PreferedLft = 300
	address.ValidLft = 600
	if err := netlink.AddrAdd(client, address); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !slaacLinkLocalReady(t, client) || !slaacLinkLocalReady(t, router) {
		if !time.Now().Before(deadline) {
			t.Fatal("link-local addresses not ready")
		}
		time.Sleep(50 * time.Millisecond)
	}
	routerInterface, err := net.InterfaceByName("slaac-r0")
	if err != nil {
		t.Fatal(err)
	}
	conn, _, err := ndp.Listen(routerInterface, ndp.LinkLocal)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.JoinGroup(netip.MustParseAddr("ff02::2")); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	raClient, err := netif.NewRAClient(iface, log)
	if err != nil {
		t.Fatal(err)
	}
	defer raClient.Close()
	module, err := slaachealth.New(slaachealth.Config{
		Iface:          iface,
		AlertAfter:     0,
		ProbeTargetsV6: []netip.Addr{netip.MustParseAddr("2001:db8:ffff::1")},
		ProbeTimeout:   100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	env := &ifmgr.Env{
		Log:    log,
		RA:     raClient,
		Sysctl: netif.NewProcSysctlRunner(log, false),
		Alerts: ifmgr.WrapNotifier(
			notify.FromConfig(notify.EmailConfig{}, notify.Config{}, log, "mwan-ifmgr"),
		),
	}
	if err := module.Init(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	solicited := make(chan error, 1)
	go func() {
		for {
			if err := conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
				solicited <- err
				return
			}
			message, _, _, err := conn.ReadFrom()
			if err != nil {
				solicited <- err
				return
			}
			if _, isSolicitation := message.(*ndp.RouterSolicitation); isSolicitation {
				solicited <- nil
				return
			}
		}
	}()
	for range 2 {
		if err := module.Reconcile(context.Background(), log); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-solicited; err != nil {
		t.Fatalf("RouterSolicitation absent: %v", err)
	}
	value, err := os.ReadFile("/proc/sys/net/ipv6/conf/" + iface + "/disable_ipv6")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(value), []byte("0")) {
		t.Fatalf("disable_ipv6 = %s, want 0", value)
	}
	addresses, err := netlink.AddrList(client, unix.AF_INET6)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range addresses {
		if current.IPNet.String() == "2001:db8:1::10/128" {
			return
		}
	}
	t.Fatalf("2001:db8:1::10/128 absent: %v", addresses)
}

func slaacLinkLocalReady(t *testing.T, link netlink.Link) bool {
	t.Helper()
	addresses, err := netlink.AddrList(link, unix.AF_INET6)
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range addresses {
		if address.IP.IsLinkLocalUnicast() && address.Flags&unix.IFA_F_TENTATIVE == 0 {
			return true
		}
	}
	return false
}
