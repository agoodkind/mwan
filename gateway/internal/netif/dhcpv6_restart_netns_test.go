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
	testDHCPv6RestartWithServer(t, false)
}

func TestDHCPv6RestartRebindWithServer(t *testing.T) {
	testDHCPv6RestartWithServer(t, true)
}

func TestDHCPv6RecoveryDoneOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	client, err := StartDHCPv6PDClient(
		ctx,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		DHCPv6PDConfig{
			Iface: "missing-dhcpv6",
			DUID:  []byte{0, 3, 0, 1, 2, 3, 4, 5, 6, 7},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.RecoveryDone():
		t.Fatal("recovery completed while the client waited for its link")
	default:
	}
	cancel()
	select {
	case <-client.RecoveryDone():
	case <-time.After(5 * time.Second):
		t.Fatal("client shutdown did not complete recovery")
	}
}

func testDHCPv6RestartWithServer(t *testing.T, requestPrefix bool) {
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
	if err := netlink.LinkAdd(&netlink.Veth{
		LinkAttrs: netlink.LinkAttrs{Name: "recovery-wan"},
		PeerName:  "recovery-srv",
	}); err != nil {
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
	config := dhcpv6RestartTestConfig(clientInterface, requestPrefix)
	cached := config.CachedLease
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
	client, err := StartDHCPv6PDClient(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), config)
	if err != nil {
		t.Fatal(err)
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
	assertDHCPv6RecoveryPending(t, client)
	assertDHCPv6RestartRequest(t, restartRequest, cached, requestPrefix)
	replyClient, err := dhcpv6.DUIDFromBytes(config.DUID)
	if err != nil {
		t.Fatal(err)
	}
	replyServer, err := dhcpv6.DUIDFromBytes(cached.ServerID)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := dhcpv6.NewMessage(
		dhcpv6.WithClientID(replyClient),
		dhcpv6.WithServerID(replyServer),
	)
	if err != nil {
		t.Fatal(err)
	}
	shortPrefix := netip.MustParsePrefix("2001:db8:31::/56")
	const shortLifetime = 3 * time.Second
	reply.MessageType = dhcpv6.MessageTypeReply
	reply.TransactionID = restartRequest.TransactionID
	reply.AddOption(&dhcpv6.OptStatusCode{StatusCode: iana.StatusSuccess})
	if requestPrefix {
		var prefixIAID [4]byte
		binary.BigEndian.PutUint32(prefixIAID[:], cached.IAID)
		association := &dhcpv6.OptIAPD{IaId: prefixIAID, T1: 30 * time.Second, T2: 60 * time.Second}
		association.Options.Add(&dhcpv6.OptIAPrefix{
			Prefix:            prefixToIPNet(cached.Prefixes[0].Prefix),
			PreferredLifetime: 90 * time.Second,
			ValidLifetime:     120 * time.Second,
		})
		// Equal lifetimes end the Solicit for the missing IA_NA at this prefix's valid deadline.
		association.Options.Add(&dhcpv6.OptIAPrefix{
			Prefix:            prefixToIPNet(shortPrefix),
			PreferredLifetime: shortLifetime,
			ValidLifetime:     shortLifetime,
		})
		reply.AddOption(association)
	}
	if _, err := server.WriteToUDP(reply.ToBytes(), source); err != nil {
		t.Fatal(err)
	}
	select {
	case <-client.RecoveryDone():
	case <-time.After(5 * time.Second):
		t.Fatal("restart validation did not complete")
	}
	validated := client.LastLease()
	if requestPrefix {
		if len(validated.Prefixes) != 2 ||
			validated.Prefixes[0].Prefix != cached.Prefixes[0].Prefix ||
			len(validated.Addresses) != 0 {
			t.Fatalf("Rebind published an unconfirmed association: %+v", validated)
		}
		assertDHCPv6ExpiryPublishedWhileAcquiring(
			t,
			client,
			cached.Prefixes[0].Prefix,
			validated.Prefixes[1].ValidUntil,
		)
		return
	}
	if len(validated.Addresses) != 1 || validated.Addresses[0] != cached.Addresses[0] ||
		validated.LinkIndex != clientInterface.Index {
		t.Fatalf("validated address or original deadlines changed: %+v", validated)
	}
}

// assertDHCPv6ExpiryPublishedWhileAcquiring requires the client to publish a lease with
// only the remaining prefix after the other prefix expires. The published lease must not
// contain the expired prefix. The server leaves the Solicit for the missing IA_NA
// unanswered during the check.
func assertDHCPv6ExpiryPublishedWhileAcquiring(
	t *testing.T,
	client *DHCPv6PDClient,
	remaining netip.Prefix,
	expiry time.Time,
) {
	t.Helper()
	deadline := expiry.Add(2 * time.Second)
	for time.Now().Before(deadline) {
		lease := client.LastLease()
		observed := time.Now()
		if len(lease.Prefixes) == 1 && lease.Prefixes[0].Prefix == remaining {
			if observed.Before(expiry) {
				t.Fatalf(
					"prefix removed %s before its valid lifetime ended: %+v",
					expiry.Sub(observed),
					lease,
				)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf(
		"expired prefix remained published %s after its valid lifetime: %+v",
		time.Since(expiry),
		client.LastLease(),
	)
}
