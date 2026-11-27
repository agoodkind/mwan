package netif

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
)

func TestDHCPRenewalRetransmissionFollowsRemainingLeaseTime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) { testDHCPRenewalSchedule(t, 400*time.Millisecond) })
}

func TestDHCPLinkFlapsKeepRenewalRetryDeadline(t *testing.T) {
	synctest.Test(
		t,
		func(t *testing.T) {
			testDHCPRenewalSchedule(t, time.Minute, 2*time.Second, 7*time.Second, 13*time.Second)
		},
	)
}

func TestDHCPReconnectBeforeT1SilentServerKeepsLeaseT1AndT2(t *testing.T) {
	synctest.Test(
		t,
		func(t *testing.T) {
			testDHCPRenewalSchedule(t, time.Minute, 700*time.Millisecond)
		},
	)
}

func TestDHCPReconnectSilentServerKeepsLeaseAfterDiscover(t *testing.T) {
	synctest.Test(
		t,
		func(t *testing.T) {
			testDHCPRenewalSchedule(t, 400*time.Millisecond, 100*time.Millisecond)
		},
	)
}

func TestDHCPReconnectBeforeT1ReplacesLeaseBeforeT2(t *testing.T) {
	synctest.Test(t, testDHCPReconnectReplacement)
}

func testDHCPReconnectReplacement(t *testing.T) {
	t.Helper()
	const renewal = 15 * time.Second
	const rebinding = 100 * time.Second
	const reconnect = 10 * time.Second
	const restartWindow = time.Minute
	server := net.IPv4(198, 51, 100, 2)
	original := net.IPv4(198, 51, 100, 101)
	replacement := net.IPv4(198, 51, 100, 102)
	started := time.Now()
	var firstVerification, firstRenewal time.Time

	reply := func(packet *dhcpv4.DHCPv4, send func(*dhcpv4.DHCPv4)) {
		address := original
		if time.Since(started) >= reconnect {
			address = replacement
			if packet.ClientIPAddr.Equal(original) {
				if firstRenewal.IsZero() {
					firstRenewal = time.Now()
				}

				return
			}

			if packet.RequestedIPAddress().Equal(original) {
				if packet.ClientIPAddr.IsUnspecified() && packet.IsBroadcast() &&
					!packet.Options.Has(
						dhcpv4.OptionServerIdentifier,
					) && firstVerification.IsZero() {
					firstVerification = time.Now()
				}

				return
			}
		}

		response := dhcpv4.MessageTypeOffer
		if packet.MessageType() == dhcpv4.MessageTypeRequest {
			response = dhcpv4.MessageTypeAck
		}

		answer, err := dhcpv4.NewReplyFromRequest(
			packet,
			dhcpv4.WithMessageType(response),
			dhcpv4.WithYourIP(address),
			dhcpv4.WithOption(dhcpv4.OptServerIdentifier(server)),
			dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
			dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(time.Hour)),
			dhcpv4.WithOption(dhcpv4.OptRenewTimeValue(renewal)),
			dhcpv4.WithOption(dhcpv4.OptRebindingTimeValue(rebinding)),
		)
		if err != nil {
			t.Error(err)

			return
		}

		send(answer)
	}

	client := newDHCPTimingClient(t, restartWindow, reply)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go client.run(ctx)
	select {
	case first := <-client.Events:
		if first.State != LeaseBound || !first.IP.Equal(original) {
			t.Fatalf("initial assignment = %v", first)
		}
	case <-time.After(reconnect):
		t.Fatal("DHCP client did not acquire the original address before reconnection")
	}

	time.AfterFunc(reconnect, client.VerifyLease)
	deadline := time.After(rebinding)

	for {
		select {
		case event := <-client.Events:
			if event.State != LeaseBound {
				continue
			}

			if !event.IP.Equal(replacement) || event.InvalidationEpoch != 1 ||
				time.Since(started) != reconnect+restartWindow {
				t.Fatalf("replacement at %v = %+v", time.Since(started), event)
			}

			if !firstVerification.Equal(started.Add(reconnect)) ||
				!firstRenewal.Equal(started.Add(renewal)) {
				t.Fatalf(
					"first verification at %v, first renewal at %v",
					firstVerification,
					firstRenewal,
				)
			}

			return
		case <-deadline:
			t.Fatalf(
				"no replacement before rebinding at %v; last lease = %v",
				rebinding,
				client.LastLease(),
			)
		}
	}
}

type renewalPacketConn struct {
	net.Conn
	sent func(*dhcpv4.DHCPv4)
}

func (conn renewalPacketConn) ReadFrom(buffer []byte) (int, net.Addr, error) {
	n, err := conn.Read(buffer)

	return n, conn.RemoteAddr(), err
}

func (conn renewalPacketConn) WriteTo(packet []byte, _ net.Addr) (int, error) {
	message, err := dhcpv4.FromBytes(packet)
	if err != nil {
		return 0, err
	}

	conn.sent(message)

	return len(packet), nil
}

func newDHCPTimingClient(
	t *testing.T,
	restartWindow time.Duration,
	reply func(*dhcpv4.DHCPv4, func(*dhcpv4.DHCPv4)),
) *DHCPClient {
	t.Helper()
	link := &net.Interface{
		Name:         "wan0",
		Index:        3,
		HardwareAddr: net.HardwareAddr{2, 0, 0, 0, 0, 1},
		Flags:        net.FlagUp | net.FlagRunning,
	}

	return &DHCPClient{
		cfg: DHCPConfig{
			Iface:              link.Name,
			InitialBackoff:     time.Second,
			MaxBackoff:         time.Second,
			RequestTimeout:     300 * time.Millisecond,
			RestartWindow:      restartWindow,
			DiscoverTimeout:    100 * time.Millisecond,
			RenewTimeout:       400 * time.Millisecond,
			MinimumRenewalWait: 12 * time.Second,
		},
		log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		clock:       realClock{},
		origin:      LeaseInfo{LinkIndex: link.Index, LinkHardwareAddr: link.HardwareAddr},
		Events:      make(chan LeaseInfo, 16),
		linkChanged: make(chan struct{}, 1),
		lookupLink:  func(string) (*net.Interface, error) { return link, nil },
		openClient: func(_ *LeaseInfo, options ...nclient4.ClientOpt) (*nclient4.Client, error) {
			wire, peer := net.Pipe()
			t.Cleanup(func() { _ = peer.Close() })
			send := func(answer *dhcpv4.DHCPv4) {
				if _, err := peer.Write(answer.ToBytes()); err != nil {
					t.Error(err)
				}
			}
			conn := renewalPacketConn{
				Conn: wire,
				sent: func(packet *dhcpv4.DHCPv4) { reply(packet, send) },
			}

			return nclient4.NewWithConn(conn, link.HardwareAddr, options...)
		},
	}
}

func testDHCPRenewalSchedule(t *testing.T, restartWindow time.Duration, flaps ...time.Duration) {
	t.Helper()
	const renewal = time.Second
	const rebinding = 30 * time.Second
	const lifetime = 44 * time.Second
	server := net.IPv4(198, 51, 100, 2)
	address := net.IPv4(198, 51, 100, 77)
	ack, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeAck),
		dhcpv4.WithYourIP(address),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(server)),
		dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
		dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(lifetime)),
		dhcpv4.WithOption(dhcpv4.OptRenewTimeValue(renewal)),
		dhcpv4.WithOption(dhcpv4.OptRebindingTimeValue(rebinding)),
	)
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	type request struct {
		after     time.Duration
		broadcast bool
	}
	var requests []request
	var verifications int
	var firstDiscover time.Time
	client := newDHCPTimingClient(
		t,
		restartWindow,
		func(packet *dhcpv4.DHCPv4, _ func(*dhcpv4.DHCPv4)) {
			if packet.MessageType() == dhcpv4.MessageTypeDiscover {
				if firstDiscover.IsZero() {
					firstDiscover = time.Now()
				}

				return
			}

			if packet.MessageType() == dhcpv4.MessageTypeRequest &&
				packet.ClientIPAddr.IsUnspecified() && packet.RequestedIPAddress().Equal(address) &&
				!packet.Options.Has(dhcpv4.OptionServerIdentifier) && packet.IsBroadcast() {
				verifications++

				return
			}

			if packet.MessageType() != dhcpv4.MessageTypeRequest ||
				!packet.ClientIPAddr.Equal(address) {
				t.Errorf("renewal packet = %s", packet.Summary())
			}

			requests = append(requests, request{time.Since(started), packet.IsBroadcast()})
		},
	)

	for _, at := range flaps {
		time.AfterFunc(at, client.VerifyLease)
	}

	client.bound(
		t.Context(),
		client.log,
		&dhcpLease{lease: &nclient4.Lease{Offer: ack, ACK: ack}, requestedAt: started},
	)
	want := []request{
		{renewal, false},
		{15700 * time.Millisecond, false},
		{28100 * time.Millisecond, false},
		{rebinding, true},
		{42400 * time.Millisecond, true},
	}
	if len(flaps) != 0 && verifications == 0 {
		t.Error("DHCP client did not verify the address after reconnection")
	}

	if len(flaps) > 0 && restartWindow < renewal {
		wantDiscover := started.Add(flaps[0] + restartWindow)
		if !firstDiscover.Equal(wantDiscover) {
			t.Errorf("first DHCP DISCOVER at %v, want %v", firstDiscover, wantDiscover)
		}
	}

	if (len(flaps) == 0 || restartWindow >= lifetime) && !firstDiscover.IsZero() {
		t.Errorf("sent DHCP DISCOVER at %v before the lease expired", firstDiscover)
	}

	if !slices.Equal(requests, want) {
		t.Errorf("requests = %+v, want %+v", requests, want)
	}

	if last := client.LastLease(); last.State != LeaseExpired || last.InvalidationEpoch != 1 ||
		time.Since(started) != lifetime {
		t.Errorf(
			"lease state after %s = %v, want expired after %s",
			time.Since(started),
			last.State,
			lifetime,
		)
	}
}

func TestNextBackoff(t *testing.T) {
	cases := []struct {
		cur  time.Duration
		max  time.Duration
		want time.Duration
	}{
		{5 * time.Second, time.Minute, 10 * time.Second},
		{30 * time.Second, time.Minute, time.Minute},
		{time.Minute, time.Minute, time.Minute},
		{2 * time.Minute, time.Minute, time.Minute},
	}
	for _, tc := range cases {
		got := nextBackoff(tc.cur, tc.max)
		if got != tc.want {
			t.Errorf("nextBackoff(%v, %v) = %v, want %v", tc.cur, tc.max, got, tc.want)
		}
	}
}

func TestLeaseToInfoNilLease(t *testing.T) {
	now := time.Now()
	info := leaseToInfo(LeaseExpired, nil, now, now)
	if info.State != LeaseExpired {
		t.Fatalf("State got %v want %v", info.State, LeaseExpired)
	}

	if info.IP != nil {
		t.Errorf("IP should be nil, got %v", info.IP)
	}

	if !info.AcquiredAt.Equal(now) {
		t.Errorf("AcquiredAt got %v want %v", info.AcquiredAt, now)
	}
}

func TestLeaseStateString(t *testing.T) {
	pairs := map[LeaseState]string{
		LeaseInit:       "INIT",
		LeaseSelecting:  "SELECTING",
		LeaseRequesting: "REQUESTING",
		LeaseBound:      "BOUND",
		LeaseRenewing:   "RENEWING",
		LeaseRebinding:  "REBINDING",
		LeaseExpired:    "EXPIRED",
	}
	for s, want := range pairs {
		if got := s.String(); got != want {
			t.Errorf("State(%d).String()=%q want %q", s, got, want)
		}
	}
}

func TestDHCPEventsPublishCurrentAssignment(t *testing.T) {
	client := &DHCPClient{
		log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Events: make(chan LeaseInfo, 1),
	}
	client.emit(LeaseInfo{
		State: LeaseBound,
		IP:    []byte{192, 0, 2, 1},
	})
	client.emit(LeaseInfo{State: LeaseExpired})
	client.emit(LeaseInfo{State: LeaseSelecting})
	select {
	case event := <-client.Events:
		if event.State != LeaseExpired || event.IP != nil || event.InvalidationEpoch != 1 {
			t.Fatalf("queued event = %v, want current expired assignment", event)
		}
	default:
		t.Fatal("current assignment was not published")
	}

	if got := client.LastLease().InvalidationEpoch; got != 1 {
		t.Fatalf("selection invalidation epoch = %d, want 1", got)
	}

	client.emit(LeaseInfo{State: LeaseBound})
	if got := client.LastLease().InvalidationEpoch; got != 1 {
		t.Fatalf("next assignment invalidation epoch = %d, want 1", got)
	}
}

func TestDHCPAckDeadlinesAndRequiredOptions(t *testing.T) {
	ack, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeAck),
		dhcpv4.WithYourIP(net.IPv4(192, 0, 2, 8)),
		dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(192, 0, 2, 1))),
		dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(80*time.Second)),
	)
	if err != nil {
		t.Fatal(err)
	}

	packet, err := dhcpv4.FromBytes(ack.ToBytes())
	if err != nil {
		t.Fatal(err)
	}

	lease := &nclient4.Lease{ACK: packet}
	if err := validateLease(lease); err != nil {
		t.Fatalf("valid ACK rejected: %v", err)
	}

	acquired := time.Now()
	info := leaseToInfo(LeaseBound, lease, acquired, acquired)
	if !info.RenewAt.Equal(acquired.Add(40*time.Second)) ||
		!info.RebindAt.Equal(acquired.Add(70*time.Second)) ||
		!info.ExpiresAt.Equal(acquired.Add(80*time.Second)) {
		t.Fatalf(
			"default deadlines: renew=%v rebind=%v expire=%v",
			info.RenewAt,
			info.RebindAt,
			info.ExpiresAt,
		)
	}

	packet.UpdateOption(dhcpv4.OptRenewTimeValue(20 * time.Second))
	packet.UpdateOption(dhcpv4.OptRebindingTimeValue(60 * time.Second))
	info = leaseToInfo(LeaseBound, lease, acquired, acquired)
	if !info.RenewAt.Equal(acquired.Add(20*time.Second)) ||
		!info.RebindAt.Equal(acquired.Add(60*time.Second)) {
		t.Fatalf("server deadlines: renew=%v rebind=%v", info.RenewAt, info.RebindAt)
	}

	packet.UpdateOption(dhcpv4.OptRenewTimeValue(75 * time.Second))
	packet.UpdateOption(dhcpv4.OptRebindingTimeValue(30 * time.Second))
	info = leaseToInfo(LeaseBound, lease, acquired, acquired)
	if !info.RenewAt.Equal(acquired.Add(40*time.Second)) ||
		!info.RebindAt.Equal(acquired.Add(70*time.Second)) {
		t.Fatalf("invalid timer fallback: renew=%v rebind=%v", info.RenewAt, info.RebindAt)
	}

	packet.UpdateOption(dhcpv4.OptIPAddressLeaseTime(0))
	if err := validateLease(lease); err == nil {
		t.Fatal("zero lease time accepted")
	}
}

func TestDHCPClasslessRoutesOverrideRouterOption(t *testing.T) {
	_, subnet, err := net.ParseCIDR("198.51.100.0/24")
	if err != nil {
		t.Fatal(err)
	}

	defaultRoute := &dhcpv4.Route{
		Dest:   &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Router: net.IPv4(192, 0, 2, 9),
	}
	subnetRoute := &dhcpv4.Route{Dest: subnet, Router: net.IPv4(192, 0, 2, 10)}
	ack, err := dhcpv4.New(
		dhcpv4.WithMessageType(dhcpv4.MessageTypeAck),
		dhcpv4.WithYourIP(net.IPv4(192, 0, 2, 8)),
		dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(192, 0, 2, 1))),
		dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(time.Minute)),
		dhcpv4.WithOption(dhcpv4.OptRouter(net.IPv4(192, 0, 2, 1))),
		dhcpv4.WithOption(dhcpv4.OptClasslessStaticRoute(defaultRoute, subnetRoute)),
	)
	if err != nil {
		t.Fatal(err)
	}

	packet, err := dhcpv4.FromBytes(ack.ToBytes())
	if err != nil {
		t.Fatal(err)
	}

	lease := &nclient4.Lease{ACK: packet}
	if err := validateLease(lease); err != nil {
		t.Fatalf("valid classless routes rejected: %v", err)
	}

	now := time.Now()
	info := leaseToInfo(LeaseBound, lease, now, now)
	if !info.Gateway.Equal(defaultRoute.Router) || len(info.Routes) != 2 ||
		info.Routes[1].Destination.String() != subnet.String() ||
		!info.Routes[1].Gateway.Equal(subnetRoute.Router) {
		t.Fatalf("classless routes did not override option 3: %+v", info)
	}

	packet.UpdateOption(dhcpv4.OptClasslessStaticRoute(subnetRoute))
	info = leaseToInfo(LeaseBound, lease, now, now)
	if info.Gateway != nil || len(info.Routes) != 1 {
		t.Fatalf("option 3 supplied a default despite option 121: %+v", info)
	}

	packet.DeleteOption(dhcpv4.OptionClasslessStaticRoute)
	info = leaseToInfo(LeaseBound, lease, now, now)
	if !info.Gateway.Equal(net.IPv4(192, 0, 2, 1)) || len(info.Routes) != 1 {
		t.Fatalf("option 3 default route missing: %+v", info)
	}

	packet.UpdateOption(dhcpv4.OptGeneric(dhcpv4.OptionClasslessStaticRoute, []byte{33}))
	if err := validateLease(lease); err == nil {
		t.Fatal("malformed classless route accepted")
	}
}

func TestDHCPRequestOptionsOnSerializedPackets(t *testing.T) {
	clientID := []byte{0xff, 0, 1, 2, 3}
	client := &DHCPClient{cfg: DHCPConfig{ClientID: clientID}}
	hardwareAddress := net.HardwareAddr{2, 0, 0, 0, 0, 1}
	discover, err := dhcpv4.NewDiscovery(hardwareAddress, client.requestModifiers()...)
	if err != nil {
		t.Fatal(err)
	}

	offer, err := dhcpv4.New(
		dhcpv4.WithHwAddr(hardwareAddress),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeOffer),
		dhcpv4.WithYourIP(net.IPv4(192, 0, 2, 8)),
		dhcpv4.WithOption(dhcpv4.OptServerIdentifier(net.IPv4(192, 0, 2, 1))),
	)
	if err != nil {
		t.Fatal(err)
	}

	request, err := dhcpv4.NewRequestFromOffer(offer, client.requestModifiers()...)
	if err != nil {
		t.Fatal(err)
	}

	ack, err := dhcpv4.New(
		dhcpv4.WithHwAddr(hardwareAddress),
		dhcpv4.WithMessageType(dhcpv4.MessageTypeAck),
		dhcpv4.WithYourIP(net.IPv4(192, 0, 2, 8)),
	)
	if err != nil {
		t.Fatal(err)
	}

	renew, err := dhcpv4.NewRenewFromAck(ack, client.requestModifiers()...)
	if err != nil {
		t.Fatal(err)
	}

	rebindModifiers := append(
		[]dhcpv4.Modifier{dhcpv4.WithBroadcast(true)},
		client.requestModifiers()...,
	)
	rebind, err := dhcpv4.NewRenewFromAck(ack, rebindModifiers...)
	if err != nil {
		t.Fatal(err)
	}

	for _, original := range []*dhcpv4.DHCPv4{discover, request, renew, rebind} {
		packet, err := dhcpv4.FromBytes(original.ToBytes())
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(packet.Options.Get(dhcpv4.OptionClientIdentifier), clientID) ||
			!packet.ParameterRequestList().Has(dhcpv4.OptionClasslessStaticRoute) {
			t.Fatalf("request options missing from %s", packet.MessageType())
		}

		requested := packet.ParameterRequestList()
		if requested[0] != dhcpv4.OptionClasslessStaticRoute ||
			!requested.Has(dhcpv4.OptionSubnetMask) || !requested.Has(dhcpv4.OptionRouter) ||
			!requested.Has(
				dhcpv4.OptionDomainName,
			) || !requested.Has(dhcpv4.OptionDomainNameServer) {
			t.Fatalf("request option order or standard options missing: %v", requested)
		}
	}

	if !rebind.IsBroadcast() {
		t.Fatal("rebind packet is not broadcast")
	}
}
