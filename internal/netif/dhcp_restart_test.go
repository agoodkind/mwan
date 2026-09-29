package netif

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/server4"
	"github.com/vishvananda/netlink"
)

func TestDHCPRestartClientWithServer(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("raw DHCP transport requires root")
	}
	const clientInterface = "dhcrs-client"
	const serverInterface = "dhcrs-server"
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: clientInterface}, PeerName: serverInterface,
	}); err != nil {
		t.Skipf("veth creation unavailable: %v", err)
	}
	t.Cleanup(func() {
		link, err := netlink.LinkByName(clientInterface)
		if err == nil {
			_ = netlink.LinkDel(link)
		}
	})
	for _, name := range []string{clientInterface, serverInterface} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
	serverLink, err := netlink.LinkByName(serverInterface)
	if err != nil {
		t.Fatal(err)
	}
	serverAddress, err := netlink.ParseAddr("192.0.2.1/24")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.AddrAdd(serverLink, serverAddress); err != nil {
		t.Fatal(err)
	}
	clientLink, err := net.InterfaceByName(clientInterface)
	if err != nil {
		t.Fatal(err)
	}
	address := net.IPv4(192, 0, 2, 8)
	clientID := []byte{0xff, 1, 2, 3}
	for _, responseType := range []dhcpv4.MessageType{
		dhcpv4.MessageTypeAck, dhcpv4.MessageTypeNak, 0,
	} {
		t.Run(responseType.String(), func(t *testing.T) {
			caseClientID := append(append([]byte(nil), clientID...), byte(responseType))
			requests := make(chan *dhcpv4.DHCPv4, 16)
			server, err := server4.NewServer(serverInterface,
				&net.UDPAddr{IP: net.IPv4zero, Port: 67},
				func(conn net.PacketConn, peer net.Addr, packet *dhcpv4.DHCPv4) {
					select {
					case requests <- packet:
					default:
					}
					if packet.MessageType() != dhcpv4.MessageTypeRequest || responseType == 0 {
						return
					}
					reply, replyErr := dhcpv4.NewReplyFromRequest(packet,
						dhcpv4.WithMessageType(responseType),
						dhcpv4.WithYourIP(address),
						dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(192, 0, 2, 1))),
						dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
						dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(time.Minute)),
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
			cached := LeaseInfo{
				IP: append(net.IP(nil), address...), LinkIndex: clientLink.Index + 100,
				LinkHardwareAddr: append(net.HardwareAddr(nil), clientLink.HardwareAddr...),
				ExpiresAt:        time.Now().Add(800 * time.Millisecond),
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			client := StartDHCPClient(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), DHCPConfig{
				Iface: clientInterface, CachedLease: &cached, ClientID: caseClientID,
				RequestTimeout: 100 * time.Millisecond, InitialBackoff: 50 * time.Millisecond,
			})
			cached.IP[0] = 203
			deadline := time.After(2 * time.Second)
			for {
				var packet *dhcpv4.DHCPv4
				select {
				case packet = <-requests:
					if !bytes.Equal(packet.Options.Get(dhcpv4.OptionClientIdentifier), caseClientID) {
						continue
					}
				case <-deadline:
					t.Fatal("restart request not received")
				}
				if packet.MessageType() != dhcpv4.MessageTypeRequest ||
					!packet.IsBroadcast() || !packet.ClientIPAddr.Equal(net.IPv4zero) ||
					!packet.RequestedIPAddress().Equal(address) ||
					packet.Options.Has(dhcpv4.OptionServerIdentifier) ||
					!bytes.Equal(packet.Options.Get(dhcpv4.OptionClientIdentifier), caseClientID) {
					t.Fatalf("invalid restart packet: %s", packet.Summary())
				}
				break
			}
			select {
			case event := <-client.Events:
				if responseType == dhcpv4.MessageTypeAck {
					if event.State != LeaseBound || !event.IP.Equal(address) ||
						event.LinkIndex != clientLink.Index || !event.ExpiresAt.After(cached.ExpiresAt) {
						t.Fatalf("ACK event = %+v", event)
					}
				} else if event.State != LeaseExpired {
					t.Fatalf("rejected or expired cache event = %+v", event)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("restart outcome not published")
			}
			if responseType == dhcpv4.MessageTypeAck {
				return
			}
			if responseType == 0 && time.Now().Before(cached.ExpiresAt) {
				t.Fatal("saved address expired before its original deadline")
			}
			deadline = time.After(2 * time.Second)
			for {
				select {
				case packet := <-requests:
					if bytes.Equal(packet.Options.Get(dhcpv4.OptionClientIdentifier), caseClientID) &&
						packet.MessageType() == dhcpv4.MessageTypeDiscover {
						return
					}
				case <-deadline:
					t.Fatal("normal DORA did not start after cache rejection")
				}
			}
		})
	}
}
