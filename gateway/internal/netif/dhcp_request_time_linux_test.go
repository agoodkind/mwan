//go:build linux && netns

package netif

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
	"golang.org/x/sys/unix"
)

func TestDHCPLeaseDeadlineAfterDelayedACK(t *testing.T) {
	const childEnvironment = "MWAN_DHCP_REQUEST_TIME_CHILD"
	if os.Getenv(childEnvironment) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(
			os.Args[0],
			"-test.v",
			"-test.run=^TestDHCPLeaseDeadlineAfterDelayedACK$",
		)
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnvironment+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("isolated DHCP deadline test: %v: %s", err, output)
		}
		return
	}
	for _, phase := range []string{
		"acquire",
		"restart",
		"renew",
		"rebind",
		"expired",
		"expired-renew",
	} {
		t.Run(
			phase,
			func(t *testing.T) {
				testDHCPLeaseDeadline(t, phase)
			},
		)
	}
}

func testDHCPLeaseDeadline(t *testing.T, phase string) {
	t.Helper()
	const clientName = "dhctime-client"
	const serverName = "dhctime-server"
	const responseDelay = 1200 * time.Millisecond
	leaseDuration := 20 * time.Second
	if phase == "expired" {
		leaseDuration = time.Second
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	gateway, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer gateway.Close()
	serverNamespace, err := netns.New()
	if err != nil {
		t.Fatal(err)
	}
	defer serverNamespace.Close()
	if err := netns.Set(gateway); err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: clientName},
		PeerName:  serverName,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		link, err := netlink.LinkByName(clientName)
		if err == nil {
			_ = netlink.LinkDel(link)
		}
	})
	serverLink, err := netlink.LinkByName(serverName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetNsFd(serverLink, int(serverNamespace)); err != nil {
		t.Fatal(err)
	}
	link, err := netlink.LinkByName(clientName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	clientLink, err := net.InterfaceByName(clientName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netns.Set(serverNamespace); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := netns.Set(gateway); err != nil {
			t.Errorf("restore network namespace: %v", err)
		}
	}()
	serverLink, err = netlink.LinkByName(serverName)
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(serverLink); err != nil {
		t.Fatal(err)
	}
	serverAddress, err := netlink.ParseAddr("192.0.2.1/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(serverLink, serverAddress); err != nil {
		t.Fatal(err)
	}
	requests := make(chan time.Time, 1)
	server, err := server4.NewServer(
		serverName,
		&net.UDPAddr{IP: net.IPv4zero, Port: 67},
		func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
			responseType := dhcpv4.MessageTypeOffer
			responseLease := leaseDuration
			if packet.MessageType() == dhcpv4.MessageTypeRequest {
				responseType = dhcpv4.MessageTypeAck
				renewing := !packet.ClientIPAddr.Equal(net.IPv4zero)
				rebinding := renewing && packet.IsBroadcast()
				if phase == "rebind" && renewing && !rebinding {
					return
				}
				target := (phase == "acquire" && !renewing) || (phase == "restart" && !renewing) ||
					(phase == "expired" && !renewing) ||
					(phase == "renew" && renewing && !rebinding) ||
					(phase == "rebind" && rebinding) ||
					(phase == "expired-renew" && renewing && !rebinding)
				if target {
					if phase == "expired-renew" {
						responseLease = time.Second
					}
					select {
					case requests <- time.Now():
					default:
					}
					time.Sleep(responseDelay)
				}
			}
			reply, replyErr := dhcpv4.NewReplyFromRequest(
				packet,
				dhcpv4.WithMessageType(responseType),
				dhcpv4.WithYourIP(net.IPv4(192, 0, 2, 8)),
				dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(192, 0, 2, 1))),
				dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
				dhcpv4.WithOption(dhcpv4.OptRenewTimeValue(2*time.Second)),
				dhcpv4.WithOption(dhcpv4.OptRebindingTimeValue(4*time.Second)),
				dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(responseLease)),
			)
			if replyErr == nil {
				_, _ = conn.WriteTo(reply.ToBytes(), peer)
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := netns.Set(gateway); err != nil {
		t.Fatal(err)
	}
	serverResult := make(chan error, 1)
	go func() { serverResult <- server.Serve() }()
	t.Cleanup(func() {
		_ = server.Close()
		if err := <-serverResult; !errors.Is(err, net.ErrClosed) {
			t.Errorf("DHCP server: %v", err)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	config := DHCPConfig{
		Iface:          clientName,
		RequestTimeout: 5 * time.Second,
		RenewTimeout:   5 * time.Second,
	}
	if phase == "restart" {
		config.CachedLease = &LeaseInfo{
			IP:               net.IPv4(192, 0, 2, 8),
			LinkHardwareAddr: clientLink.HardwareAddr,
			ExpiresAt:        time.Now().Add(time.Minute),
		}
	}
	client := StartDHCPClient(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), config)
	deadline := time.After(15 * time.Second)
	assigned := false
	for {
		select {
		case lease := <-client.Events:
			if phase == "expired" || phase == "expired-renew" && assigned {
				if lease.State == LeaseBound {
					t.Fatal("an expired ACK produced a bound lease")
				}
				if lease.State == LeaseExpired {
					return
				}
			}
			if lease.State != LeaseBound {
				continue
			}
			select {
			case received := <-requests:
				if lease.AcquiredAt.Before(received.Add(responseDelay - 250*time.Millisecond)) {
					t.Fatal("the lease acquisition time precedes the delayed ACK")
				}
				latestExpiry := received.Add(leaseDuration + 250*time.Millisecond)
				if lease.RenewAt.After(received.Add(2250*time.Millisecond)) ||
					lease.RebindAt.After(
						received.Add(4250*time.Millisecond),
					) || lease.ExpiresAt.After(latestExpiry) {
					t.Fatalf(
						"lease deadlines after request: renewal=%s rebinding=%s expiry=%s",
						lease.RenewAt.Sub(received),
						lease.RebindAt.Sub(received),
						lease.ExpiresAt.Sub(received),
					)
				}
				return
			default:
			}
			if !assigned && (phase == "renew" || phase == "rebind" || phase == "expired-renew") {
				link, err := netlink.LinkByName(clientName)
				if err != nil {
					t.Fatal(err)
				}
				address, err := netlink.ParseAddr("192.0.2.8/24")
				if err != nil {
					t.Fatal(err)
				}
				if err := netlink.AddrAdd(link, address); err != nil {
					t.Fatal(err)
				}
				assigned = true
			}
		case <-deadline:
			t.Fatalf("delayed %s ACK did not produce a bound lease: %+v", phase, client.LastLease())
		}
	}
}
