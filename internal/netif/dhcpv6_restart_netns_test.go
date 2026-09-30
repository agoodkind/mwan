//go:build linux && netns

package netif

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/iana"
	"github.com/vishvananda/netlink"
	"golang.org/x/net/ipv6"
	"golang.org/x/sys/unix"
)

func TestDHCPv6RestartConfirmWithServer(t *testing.T) {
	testDHCPv6RestartWithServer(t, false, false)
}

func TestDHCPv6RestartRebindWithServer(t *testing.T) {
	testDHCPv6RestartWithServer(t, true, false)
}

func TestDHCPv6RestartConfirmRetransmission(t *testing.T) {
	testDHCPv6RestartWithServer(t, false, true)
}

func TestDHCPv6RestartRebindRetransmission(t *testing.T) {
	testDHCPv6RestartWithServer(t, true, true)
}

func testDHCPv6RestartWithServer(t *testing.T, requestPrefix, timeoutRecovery bool) {
	t.Helper()
	const childEnv = "MWAN_DHCPV6_RESTART_CHILD"
	if os.Getenv(childEnv) != "1" {
		if os.Geteuid() != 0 {
			t.Skip("network namespace requires root")
		}
		child := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
		child.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
		child.Env = append(os.Environ(), childEnv+"=1")
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("DHCPv6 restart namespace: %v: %s", err, output)
		}
		return
	}
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: netlink.LinkAttrs{Name: "recovery-wan"}, PeerName: "recovery-srv"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"recovery-wan", "recovery-srv"} {
		link, err := netlink.LinkByName(name)
		if err != nil {
			t.Fatal(err)
		}
		if err := netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
	}
	serverInterface, err := net.InterfaceByName("recovery-srv")
	if err != nil {
		t.Fatal(err)
	}
	serverAddress := &net.UDPAddr{IP: net.IPv6unspecified, Port: dhcpv6.DefaultServerPort}
	clientInterface, err := net.InterfaceByName("recovery-wan")
	if err != nil {
		t.Fatal(err)
	}
	duid := []byte{0, 3, 0, 1, 2, 3, 4, 5, 6, 7}
	serverDUID := []byte{0, 3, 0, 1, 2, 3, 4, 5, 6, 8}
	now := time.Now()
	assigned := netip.MustParseAddr("2001:db8::100")
	cached := DHCPv6PDLease{
		LinkName: clientInterface.Name, LinkIndex: clientInterface.Index - 1,
		LinkHardwareAddr: append(net.HardwareAddr(nil), clientInterface.HardwareAddr...),
		DUID:             append([]byte(nil), duid...), IAID: 19, IANAIAID: 17,
		ServerID: append([]byte(nil), serverDUID...), AcquiredAt: now.Add(-time.Second),
		IANARenewAt: now.Add(20 * time.Second), IANARebindAt: now.Add(30 * time.Second),
		RequestAddress: true, RequestPrefix: false,
		Addresses: []DelegatedAddress{{Address: assigned, PreferredUntil: now.Add(35 * time.Second), ValidUntil: now.Add(40 * time.Second)}},
	}
	if requestPrefix {
		cached.RequestPrefix = true
		cached.RenewAt = now.Add(20 * time.Second)
		cached.RebindAt = now.Add(30 * time.Second)
		cached.Prefixes = []DelegatedPrefix{{Prefix: netip.MustParsePrefix("2001:db8:30::/56"), PreferredUntil: now.Add(35 * time.Second), ValidUntil: now.Add(40 * time.Second)}}
	}
	server, err := net.ListenUDP("udp6", serverAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	group := &net.UDPAddr{IP: net.ParseIP("ff02::1:2")}
	if err := ipv6.NewPacketConn(server).JoinGroup(serverInterface, group); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client, err := StartDHCPv6PDClient(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), DHCPv6PDConfig{
		Iface: clientInterface.Name, DUID: duid, IAID: 19, IANAIAID: 17,
		RequestAddress: true, RequestPrefix: requestPrefix, Hint: netip.Prefix{},
		Clock: realClock{}, WaitForRA: false, CachedLease: &cached,
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if got := client.LastLease(); len(got.Addresses) != 0 {
		t.Fatalf("cached address published before validation: %+v", got)
	}
	request := make([]byte, 1500)
	if err := server.SetReadDeadline(time.Now().Add(15 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, source, err := server.ReadFromUDP(request)
	if err != nil {
		t.Fatal(err)
	}
	restartRequest, err := dhcpv6.MessageFromBytes(request[:n])
	if err != nil {
		t.Fatal(err)
	}
	if restartRequest.Options.ServerID() != nil || len(restartRequest.Options.IANA()) != 1 {
		t.Fatalf("unexpected restart request options: %s", restartRequest)
	}
	if binary.BigEndian.Uint32(restartRequest.Options.IANA()[0].IaId[:]) != 17 {
		t.Fatalf("IA_NA IAID changed: %s", restartRequest)
	}
	if requestPrefix {
		if restartRequest.MessageType != dhcpv6.MessageTypeRebind || len(restartRequest.Options.IAPD()) != 1 || binary.BigEndian.Uint32(restartRequest.Options.IAPD()[0].IaId[:]) != 19 {
			t.Fatalf("Rebind omitted cached IA_PD: %s", restartRequest)
		}
		if len(restartRequest.Options.IAPD()[0].Options.Prefixes()) != 1 {
			t.Fatalf("Rebind omitted cached prefix: %s", restartRequest)
		}
	} else if restartRequest.MessageType != dhcpv6.MessageTypeConfirm || len(restartRequest.Options.IAPD()) != 0 {
		t.Fatalf("Confirm included IA_PD: %s", restartRequest)
	}
	if timeoutRecovery {
		assertDHCPv6RestartRetransmission(t, server, client, restartRequest)
		return
	}
	replyClient, err := dhcpv6.DUIDFromBytes(duid)
	if err != nil {
		t.Fatal(err)
	}
	replyServer, err := dhcpv6.DUIDFromBytes(serverDUID)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := dhcpv6.NewMessage(dhcpv6.WithClientID(replyClient), dhcpv6.WithServerID(replyServer))
	if err != nil {
		t.Fatal(err)
	}
	reply.MessageType = dhcpv6.MessageTypeReply
	reply.TransactionID = restartRequest.TransactionID
	reply.AddOption(&dhcpv6.OptStatusCode{StatusCode: iana.StatusSuccess})
	if requestPrefix {
		var prefixIAID [4]byte
		binary.BigEndian.PutUint32(prefixIAID[:], cached.IAID)
		association := &dhcpv6.OptIAPD{IaId: prefixIAID, T1: 30 * time.Second, T2: 60 * time.Second}
		association.Options.Add(&dhcpv6.OptIAPrefix{Prefix: prefixToIPNet(cached.Prefixes[0].Prefix), PreferredLifetime: 90 * time.Second, ValidLifetime: 120 * time.Second})
		reply.AddOption(association)
	}
	if _, err := server.WriteToUDP(reply.ToBytes(), source); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		validated := client.LastLease()
		if requestPrefix && len(validated.Prefixes) == 1 {
			if validated.Prefixes[0].Prefix != cached.Prefixes[0].Prefix || len(validated.Addresses) != 0 {
				t.Fatalf("Rebind published an unconfirmed association: %+v", validated)
			}
			return
		}
		if len(validated.Addresses) == 1 {
			if validated.Addresses[0] != cached.Addresses[0] || validated.LinkIndex != clientInterface.Index {
				t.Fatalf("validated address or original deadlines changed: %+v", validated)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("restart Reply did not publish the validated association")
}

func assertDHCPv6RestartRetransmission(t *testing.T, server *net.UDPConn, client *DHCPv6PDClient, first *dhcpv6.Message) {
	t.Helper()
	firstReceived := time.Now()
	previousElapsed := uint16(0)
	previousReceived := firstReceived
	transmissions := 1
	request := make([]byte, 1500)
	for {
		n, _, err := server.ReadFromUDP(request)
		if err != nil {
			t.Fatal(err)
		}
		message, err := dhcpv6.MessageFromBytes(request[:n])
		if err != nil {
			t.Fatal(err)
		}
		if message.MessageType == dhcpv6.MessageTypeSolicit {
			elapsed := time.Since(firstReceived)
			if transmissions < 3 || elapsed < 9*time.Second || elapsed > 12*time.Second {
				t.Fatalf("restart exchange ended outside 10-second MRD: transmissions=%d elapsed=%s", transmissions, elapsed)
			}
			if got := client.LastLease(); len(got.Prefixes) != 0 || len(got.Addresses) != 0 {
				t.Fatalf("unvalidated cache published after restart timeout: %+v", got)
			}
			return
		}
		if message.MessageType != first.MessageType || message.TransactionID != first.TransactionID || message.Options.ServerID() != nil {
			t.Fatalf("restart retransmission changed identity or type: %s", message)
		}
		option := message.GetOneOption(dhcpv6.OptionElapsedTime)
		if option == nil || len(option.ToBytes()) != 2 {
			t.Fatalf("restart retransmission omitted elapsed time: %s", message)
		}
		elapsed := binary.BigEndian.Uint16(option.ToBytes())
		if elapsed <= previousElapsed {
			t.Fatalf("elapsed time did not increase: previous=%d current=%d", previousElapsed, elapsed)
		}
		gap := time.Since(previousReceived)
		if transmissions == 1 && (gap < 700*time.Millisecond || gap > 1500*time.Millisecond) {
			t.Fatalf("initial restart retransmission delay = %s", gap)
		}
		if transmissions == 2 && (gap < 1500*time.Millisecond || gap > 2800*time.Millisecond) {
			t.Fatalf("second restart retransmission delay = %s", gap)
		}
		if transmissions > 2 && gap > 5*time.Second {
			t.Fatalf("restart retransmission exceeded MRT: %s", gap)
		}
		previousElapsed = elapsed
		previousReceived = time.Now()
		transmissions++
	}
}
