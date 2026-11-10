//go:build linux && netns

package netif_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/netif"
)

const (
	renewalTimingChildEnv  = "MWAN_DHCP_RENEWAL_TIMING_CHILD"
	renewalTimingClient    = "dhrenew-c0"
	renewalTimingServer    = "dhrenew-s0"
	renewalTimingCIDR      = "198.51.100.77/24"
	renewalTimingT1        = 5 * time.Second
	renewalTimingT2        = 150 * time.Second
	renewalTimingLease     = 220 * time.Second
	renewalTimingAttempt   = 2 * time.Second
	renewalTimingTolerance = 3 * time.Second
)

type renewalTimingRequest struct {
	at        time.Time
	broadcast bool
}

func TestDHCPRenewalRetransmissionFollowsRemainingLeaseTime(t *testing.T) {
	if os.Getenv(renewalTimingChildEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("The test requires root to create network namespaces.")
		}
		child := exec.Command(os.Args[0], "-test.v", "-test.run=^"+t.Name()+"$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), renewalTimingChildEnv+"=1")
		output, err := child.CombinedOutput()
		t.Logf("%s", output)
		if err != nil {
			t.Fatalf("Child test process %s failed: %v", t.Name(), err)
		}
		return
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	clientNamespace, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer clientNamespace.Close()
	serverNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer serverNamespace.Close()
	if err := netns.Set(clientNamespace); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: renewalTimingClient}, PeerName: renewalTimingServer,
	}); err != nil {
		t.Fatal(err)
	}
	clientLink, err := netlink.LinkByName(renewalTimingClient)
	if err != nil {
		t.Fatal(err)
	}
	serverLink, err := netlink.LinkByName(renewalTimingServer)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(serverLink, int(serverNamespace)); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(clientLink); err != nil {
		t.Fatal(err)
	}
	if err := netns.Set(serverNamespace); err != nil {
		t.Fatal(err)
	}
	serverLink, err = netlink.LinkByName(renewalTimingServer)
	if err != nil {
		t.Fatal(err)
	}
	serverAddress, err := netlink.ParseAddr("198.51.100.2/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(serverLink, serverAddress); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(serverLink); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var requests []renewalTimingRequest
	server, err := server4.NewServer(renewalTimingServer, &net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			if packet.MessageType() == dhcpv4.MessageTypeRequest && !packet.ClientIPAddr.IsUnspecified() {
				mu.Lock()
				requests = append(requests, renewalTimingRequest{at: time.Now(), broadcast: packet.IsBroadcast()})
				mu.Unlock()
				return
			}
			responseType := dhcpv4.MessageTypeOffer
			if packet.MessageType() == dhcpv4.MessageTypeRequest {
				responseType = dhcpv4.MessageTypeAck
			}
			reply, replyErr := dhcpv4.NewReplyFromRequest(packet,
				dhcpv4.WithMessageType(responseType),
				dhcpv4.WithYourIP(net.ParseIP("198.51.100.77")),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.ParseIP("198.51.100.2"))),
				dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
				dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(renewalTimingLease)),
				dhcpv4.WithOption(dhcpv4.OptRenewTimeValue(renewalTimingT1)),
				dhcpv4.WithOption(dhcpv4.OptRebindingTimeValue(renewalTimingT2)),
			)
			if replyErr == nil {
				_, _ = conn.WriteTo(reply.ToBytes(), peer)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve() }()
	t.Cleanup(func() { _ = server.Close() })
	if err := netns.Set(clientNamespace); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := netif.StartDHCPClient(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), netif.DHCPConfig{
		Iface:          renewalTimingClient,
		InitialBackoff: time.Second,
		RenewTimeout:   renewalTimingAttempt,
	})
	var boundAt time.Time
	var expiredAt time.Time
	deadline := time.After(renewalTimingLease + 30*time.Second)
	for expiredAt.IsZero() {
		select {
		case event := <-client.Events:
			switch event.State {
			case netif.LeaseBound:
				if !boundAt.IsZero() {
					t.Fatalf("The client emitted a second bound event: %+v", event)
				}
				boundAt = event.AcquiredAt
				address, err := netlink.ParseAddr(renewalTimingCIDR)
				if err != nil {
					t.Fatal(err)
				}
				if err := netlink.AddrAdd(clientLink, address); err != nil {
					t.Fatal(err)
				}
			case netif.LeaseExpired:
				expiredAt = time.Now()
			}
		case <-deadline:
			t.Fatalf("The client did not emit a lease expiry event before the deadline. Last lease: %+v", client.LastLease())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	var observed []time.Duration
	for _, request := range requests {
		observed = append(observed, request.at.Sub(boundAt).Round(100*time.Millisecond))
	}
	t.Logf("Request times after the bound event: %v. Lease expiry time after the bound event: %s.", observed, expiredAt.Sub(boundAt).Round(100*time.Millisecond))
	want := []struct {
		at        time.Duration
		broadcast bool
	}{
		{renewalTimingT1, false},
		{78500 * time.Millisecond, false},
		{140500 * time.Millisecond, false},
		{renewalTimingT2, true},
		{212 * time.Second, true},
	}
	if len(requests) != len(want) {
		t.Fatalf("The server received %d requests; expected %d. Request times after the bound event: %v.", len(requests), len(want), observed)
	}
	for i, expected := range want {
		difference := observed[i] - expected.at
		if difference < -renewalTimingTolerance || difference > renewalTimingTolerance {
			t.Errorf("Request %d arrived %s after the bound event; expected %s.", i, observed[i], expected.at)
		}
		if requests[i].broadcast != expected.broadcast {
			t.Errorf("Request %d had broadcast flag %t; expected %t.", i, requests[i].broadcast, expected.broadcast)
		}
	}
	difference := expiredAt.Sub(boundAt) - renewalTimingLease
	if difference < -renewalTimingTolerance || difference > renewalTimingTolerance {
		t.Errorf("The lease expired %s after the bound event; expected %s.", expiredAt.Sub(boundAt), renewalTimingLease)
	}
}
