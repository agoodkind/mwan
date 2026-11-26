package netif

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"testing"
	"testing/synctest"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
)

func TestDeclineAddressWaitsForQueueCapacity(t *testing.T) {
	address := netip.MustParseAddr("2001:db8::4")
	var client DHCPv6PDClient
	client.lease.Addresses = []DelegatedAddress{
		{Address: address, PreferredUntil: time.Time{}, ValidUntil: time.Time{}},
	}
	client.declines = make(chan netip.Addr, 1)
	client.pending = make(map[netip.Addr]bool)
	client.declines <- netip.MustParseAddr("2001:db8::5")
	result := make(chan bool, 1)
	go func() {
		result <- client.DeclineAddress(address)
	}()
	select {
	case accepted := <-result:
		t.Fatalf("full queue returned before capacity was available: accepted=%t", accepted)
	case <-time.After(20 * time.Millisecond):
	}

	snapshot := make(chan DHCPv6PDLease, 1)
	go func() {
		snapshot <- client.LastLease()
	}()
	select {
	case lease := <-snapshot:
		if len(lease.Addresses) != 1 || lease.Addresses[0].Address != address {
			t.Fatalf("client lease changed while decline waited: %+v", lease)
		}
	case <-time.After(time.Second):
		t.Fatal("client lease read blocked behind the full decline queue")
	}

	<-client.declines
	select {
	case accepted := <-result:
		if !accepted {
			t.Fatal("decline was rejected after queue capacity became available")
		}
	case <-time.After(time.Second):
		t.Fatal("decline did not queue after capacity became available")
	}
}

func TestDHCPv6RestartConfirmRetransmission(t *testing.T) {
	testDHCPv6RestartRetransmission(t, false)
}

func TestDHCPv6RestartRebindRetransmission(t *testing.T) {
	testDHCPv6RestartRetransmission(t, true)
}

func dhcpv6RestartTestConfig(link *net.Interface, requestPrefix bool) DHCPv6PDConfig {
	now := time.Now()
	duid := []byte{0, 3, 0, 1, 2, 3, 4, 5, 6, 7}
	cached := &DHCPv6PDLease{
		LinkName:         link.Name,
		LinkIndex:        link.Index - 1,
		LinkHardwareAddr: bytes.Clone(link.HardwareAddr),
		DUID:             bytes.Clone(duid),
		IAID:             19,
		IANAIAID:         17,
		ServerID:         []byte{0, 3, 0, 1, 2, 3, 4, 5, 6, 8},
		AcquiredAt:       now.Add(-time.Second),
		IANARenewAt:      now.Add(20 * time.Second),
		IANARebindAt:     now.Add(30 * time.Second),
		RequestAddress:   true,
		RequestPrefix:    requestPrefix,
		Addresses: []DelegatedAddress{
			{
				Address:        netip.MustParseAddr("2001:db8::100"),
				PreferredUntil: now.Add(35 * time.Second),
				ValidUntil:     now.Add(40 * time.Second),
			},
		},
	}
	if requestPrefix {
		cached.RenewAt = now.Add(20 * time.Second)
		cached.RebindAt = now.Add(30 * time.Second)
		cached.Prefixes = []DelegatedPrefix{
			{
				Prefix:         netip.MustParsePrefix("2001:db8:30::/56"),
				PreferredUntil: now.Add(35 * time.Second),
				ValidUntil:     now.Add(40 * time.Second),
			},
		}
	}

	return DHCPv6PDConfig{
		Iface:          link.Name,
		DUID:           duid,
		IAID:           cached.IAID,
		IANAIAID:       cached.IANAIAID,
		RequestAddress: true,
		RequestPrefix:  requestPrefix,
		Clock:          realClock{},
		CachedLease:    cached,
	}
}

type dhcpv6TestPacketConn struct {
	net.Conn
}

func (conn dhcpv6TestPacketConn) ReadFrom(packet []byte) (int, net.Addr, error) {
	n, err := conn.Read(packet)

	return n, conn.RemoteAddr(), err
}

func (conn dhcpv6TestPacketConn) WriteTo(packet []byte, _ net.Addr) (int, error) {
	return conn.Write(packet)
}

func testDHCPv6RestartRetransmission(t *testing.T, requestPrefix bool) {
	t.Helper()
	synctest.Test(
		t,
		func(t *testing.T) {
			now := time.Now()
			link := &net.Interface{
				Index:        2,
				Name:         "mwan-dhcpv6-test",
				HardwareAddr: net.HardwareAddr{2, 3, 4, 5, 6, 7},
			}
			config := dhcpv6RestartTestConfig(link, requestPrefix)
			cached := config.CachedLease
			cached.IANARenewAt = now.Add(8 * time.Second)
			cached.IANARebindAt = now.Add(16 * time.Second)
			cached.Addresses[0].PreferredUntil = now.Add(20 * time.Second)
			cached.Addresses[0].ValidUntil = now.Add(24 * time.Second)
			if requestPrefix {
				cached.RenewAt = now.Add(4 * time.Second)
				cached.RebindAt = now.Add(8 * time.Second)
				cached.Prefixes[0].PreferredUntil = now.Add(12 * time.Second)
				cached.Prefixes[0].ValidUntil = now.Add(14 * time.Second)
			}

			recoveryClient, recoveryServer := net.Pipe()
			defer recoveryServer.Close()
			defer recoveryClient.Close()
			acquisitionClient, acquisitionServer := net.Pipe()
			defer acquisitionServer.Close()
			defer acquisitionClient.Close()

			for _, server := range []net.Conn{recoveryServer, acquisitionServer} {
				if err := server.SetDeadline(now.Add(35 * time.Second)); err != nil {
					t.Fatal(err)
				}
			}

			connections := []net.Conn{recoveryClient, acquisitionClient}
			open := func(_ string, options ...nclient6.ClientOpt) (*nclient6.Client, error) {
				if len(connections) == 0 {
					return nil, net.ErrClosed
				}

				conn := connections[0]
				connections = connections[1:]

				return nclient6.NewWithConn(
					dhcpv6TestPacketConn{Conn: conn},
					link.HardwareAddr,
					options...,
				)
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			client := &DHCPv6PDClient{
				declines:     make(chan netip.Addr, 1),
				done:         ctx.Done(),
				recoveryDone: make(chan struct{}),
				wake:         make(chan struct{}, 1),
				Events:       make(chan struct{}, 1),
			}
			go client.run(
				ctx,
				log,
				dhcpv6Runtime{
					DHCPv6PDConfig: config,
					lookup:         func(string) (*net.Interface, error) { return link, nil },
					open:           open,
				},
			)
			assertDHCPv6RestartRetransmission(t, recoveryServer, client, cached)
			assertDHCPv6AcquisitionAfterRestartTimeout(t, acquisitionServer, client, cached)
		},
	)
}

func assertDHCPv6RecoveryPending(t *testing.T, client *DHCPv6PDClient) {
	t.Helper()
	if lease := client.LastLease(); len(lease.Addresses) != 0 || len(lease.Prefixes) != 0 {
		t.Fatalf("client published a cached assignment before validation: %+v", lease)
	}

	select {
	case <-client.Events:
		t.Fatal("client published a lease during restart validation")
	case <-client.RecoveryDone():
		t.Fatal("recovery completed before restart validation")
	default:
	}
}

func assertDHCPv6RestartRequest(
	t *testing.T,
	request *dhcpv6.Message,
	cached *DHCPv6PDLease,
	requestPrefix bool,
) {
	t.Helper()
	clientID := request.Options.ClientID()
	if clientID == nil || !bytes.Equal(clientID.ToBytes(), cached.DUID) ||
		request.Options.ServerID() != nil {
		t.Fatalf("unexpected restart client or server ID: %s", request.Summary())
	}

	addresses := request.Options.IANA()
	if len(addresses) != 1 || binary.BigEndian.Uint32(addresses[0].IaId[:]) != cached.IANAIAID {
		t.Fatalf("unexpected restart IA_NA: %s", request.Summary())
	}

	assigned := addresses[0].Options.Addresses()
	if len(assigned) != 1 ||
		!assigned[0].IPv6Addr.Equal(net.IP(cached.Addresses[0].Address.AsSlice())) {
		t.Fatalf("restart request omitted cached address: %s", request.Summary())
	}

	prefixes := request.Options.IAPD()
	if !requestPrefix {
		if request.MessageType != dhcpv6.MessageTypeConfirm || len(prefixes) != 0 {
			t.Fatalf("expected Confirm without IA_PD: %s", request.Summary())
		}

		return
	}

	if request.MessageType != dhcpv6.MessageTypeRebind || len(prefixes) != 1 ||
		binary.BigEndian.Uint32(prefixes[0].IaId[:]) != cached.IAID {
		t.Fatalf("expected Rebind with cached IA_PD: %s", request.Summary())
	}

	delegated := prefixes[0].Options.Prefixes()
	if len(delegated) != 1 || delegated[0].Prefix.String() != cached.Prefixes[0].Prefix.String() {
		t.Fatalf("Rebind omitted cached prefix: %s", request.Summary())
	}
}

func assertDHCPv6RestartRetransmission(
	t *testing.T,
	server net.Conn,
	client *DHCPv6PDClient,
	cached *DHCPv6PDLease,
) {
	t.Helper()
	packet := make([]byte, 1500)
	var firstReceived, exchangeStarted, previousReceived time.Time
	var xid dhcpv6.TransactionID
	exchanges, transmissions := 0, 0
	sawConfirm := false

	for {
		n, err := server.Read(packet)
		if err == io.EOF {
			break
		}

		if err != nil {
			t.Fatal(err)
		}

		received := time.Now()
		message, err := dhcpv6.MessageFromBytes(packet[:n])
		if err != nil {
			t.Fatal(err)
		}

		assertDHCPv6RecoveryPending(t, client)
		if !received.Before(cached.Addresses[0].ValidUntil) {
			t.Fatalf("restart sent %s after cached address validity ended", message.MessageType)
		}

		prefixValid := cached.RequestPrefix && received.Before(cached.Prefixes[0].ValidUntil)
		assertDHCPv6RestartRequest(t, message, cached, prefixValid)
		sawConfirm = sawConfirm || message.MessageType == dhcpv6.MessageTypeConfirm
		if exchanges == 0 || xid != message.TransactionID {
			if exchanges == 0 {
				firstReceived = received
			}

			if exchanges == 1 {
				elapsed := received.Sub(firstReceived)
				if transmissions < 3 || elapsed < 9*time.Second || elapsed > 16*time.Second {
					t.Fatalf(
						"first restart exchange ended outside its bounds: transmissions=%d elapsed=%s",
						transmissions,
						elapsed,
					)
				}
			}

			exchanges++
			xid = message.TransactionID
			exchangeStarted = received
			transmissions = 0
		} else if exchanges == 1 {
			assertDHCPv6RestartRetryGap(t, transmissions, received.Sub(previousReceived))
		}

		option := message.GetOneOption(dhcpv6.OptionElapsedTime)
		const centisecond = 10 * time.Millisecond
		wantElapsed := uint16(received.Sub(exchangeStarted).Round(centisecond) / centisecond)
		if option == nil || len(option.ToBytes()) != 2 ||
			binary.BigEndian.Uint16(option.ToBytes()) != wantElapsed {
			t.Fatalf(
				"restart transmission did not report %d hundredths of a second: %s",
				wantElapsed,
				message.Summary(),
			)
		}

		previousReceived = received
		transmissions++
	}

	if exchanges < 2 || time.Now().Before(cached.Addresses[0].ValidUntil) || !sawConfirm {
		t.Fatalf(
			"restart ended before cache expiry or missed Confirm: exchanges=%d time=%s saw_confirm=%t",
			exchanges,
			time.Now(),
			sawConfirm,
		)
	}
}

func assertDHCPv6RestartRetryGap(t *testing.T, transmissions int, gap time.Duration) {
	t.Helper()
	switch {
	case transmissions == 1 && (gap < 700*time.Millisecond || gap > 1500*time.Millisecond):
		t.Fatalf("initial restart retransmission delay = %s", gap)
	case transmissions == 2 && (gap < 1500*time.Millisecond || gap > 2800*time.Millisecond):
		t.Fatalf("second restart retransmission delay = %s", gap)
	case transmissions > 2 && gap > 5*time.Second:
		t.Fatalf("restart retransmission exceeded MRT: %s", gap)
	}
}

func assertDHCPv6AcquisitionAfterRestartTimeout(
	t *testing.T,
	server net.Conn,
	client *DHCPv6PDClient,
	cached *DHCPv6PDLease,
) {
	t.Helper()
	solicit := readDHCPv6TestMessage(t, server)
	if solicit.MessageType != dhcpv6.MessageTypeSolicit {
		t.Fatalf("expected Solicit after restart timeout, got %s", solicit.Summary())
	}

	if lease := client.LastLease(); len(lease.Addresses) != 0 || len(lease.Prefixes) != 0 {
		t.Fatalf("client published an unvalidated assignment after restart timeout: %+v", lease)
	}

	select {
	case <-client.RecoveryDone():
	default:
		t.Fatal("ordinary Solicit preceded recovery completion")
	}

	newAddress := netip.MustParseAddr("2001:db8::200")
	newPrefix := netip.MustParsePrefix("2001:db8:40::/56")
	clientID, err := dhcpv6.DUIDFromBytes(cached.DUID)
	if err != nil {
		t.Fatal(err)
	}

	serverID, err := dhcpv6.DUIDFromBytes(cached.ServerID)
	if err != nil {
		t.Fatal(err)
	}

	respond := func(request *dhcpv6.Message, messageType dhcpv6.MessageType) {
		t.Helper()
		response := &dhcpv6.Message{MessageType: messageType, TransactionID: request.TransactionID}
		dhcpv6.WithClientID(clientID)(response)
		dhcpv6.WithServerID(serverID)(response)
		var addressIAID [4]byte
		binary.BigEndian.PutUint32(addressIAID[:], cached.IANAIAID)
		addressAssociation := &dhcpv6.OptIANA{
			IaId: addressIAID,
			T1:   30 * time.Second,
			T2:   60 * time.Second,
		}
		addressAssociation.Options.Add(&dhcpv6.OptIAAddress{
			IPv6Addr:          net.IP(newAddress.AsSlice()),
			PreferredLifetime: 90 * time.Second,
			ValidLifetime:     120 * time.Second,
		})
		response.AddOption(addressAssociation)
		if cached.RequestPrefix {
			var prefixIAID [4]byte
			binary.BigEndian.PutUint32(prefixIAID[:], cached.IAID)
			prefixAssociation := &dhcpv6.OptIAPD{
				IaId: prefixIAID,
				T1:   30 * time.Second,
				T2:   60 * time.Second,
			}
			prefixAssociation.Options.Add(&dhcpv6.OptIAPrefix{
				Prefix:            prefixToIPNet(newPrefix),
				PreferredLifetime: 90 * time.Second,
				ValidLifetime:     120 * time.Second,
			})
			response.AddOption(prefixAssociation)
		}

		if _, err := server.Write(response.ToBytes()); err != nil {
			t.Fatal(err)
		}
	}
	respond(solicit, dhcpv6.MessageTypeAdvertise)
	request := readDHCPv6TestMessage(t, server)
	if request.MessageType != dhcpv6.MessageTypeRequest {
		t.Fatalf("expected Request after Advertise, got %s", request.Summary())
	}

	respond(request, dhcpv6.MessageTypeReply)
	synctest.Wait()
	lease := client.LastLease()
	if len(lease.Addresses) != 1 || lease.Addresses[0].Address != newAddress ||
		cached.RequestPrefix != (len(lease.Prefixes) == 1) ||
		(len(lease.Prefixes) != 0 && lease.Prefixes[0].Prefix != newPrefix) {
		t.Fatalf("ordinary acquisition published unexpected lease: %+v", lease)
	}
}

func readDHCPv6TestMessage(t *testing.T, server net.Conn) *dhcpv6.Message {
	t.Helper()
	packet := make([]byte, 1500)
	n, err := server.Read(packet)
	if err != nil {
		t.Fatal(err)
	}

	message, err := dhcpv6.MessageFromBytes(packet[:n])
	if err != nil {
		t.Fatal(err)
	}

	return message
}
