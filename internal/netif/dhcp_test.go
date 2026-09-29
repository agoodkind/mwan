package netif

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
	"github.com/insomniacslk/dhcp/dhcpv4/nclient4"
)

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
			t.Errorf("nextBackoff(%v, %v) = %v, want %v",
				tc.cur, tc.max, got, tc.want)
		}
	}
}

func TestLeaseToInfoNilLease(t *testing.T) {
	now := time.Now()
	info := leaseToInfo(LeaseExpired, nil, now)
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
	client.emit(LeaseInfo{State: LeaseBound, IP: []byte{192, 0, 2, 1}})
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
	info := leaseToInfo(LeaseBound, lease, acquired)
	if !info.RenewAt.Equal(acquired.Add(40*time.Second)) ||
		!info.RebindAt.Equal(acquired.Add(70*time.Second)) ||
		!info.ExpiresAt.Equal(acquired.Add(80*time.Second)) {
		t.Fatalf("default deadlines: renew=%v rebind=%v expire=%v",
			info.RenewAt, info.RebindAt, info.ExpiresAt)
	}
	packet.UpdateOption(dhcpv4.OptRenewTimeValue(20 * time.Second))
	packet.UpdateOption(dhcpv4.OptRebindingTimeValue(60 * time.Second))
	info = leaseToInfo(LeaseBound, lease, acquired)
	if !info.RenewAt.Equal(acquired.Add(20*time.Second)) ||
		!info.RebindAt.Equal(acquired.Add(60*time.Second)) {
		t.Fatalf("server deadlines: renew=%v rebind=%v", info.RenewAt, info.RebindAt)
	}
	packet.UpdateOption(dhcpv4.OptRenewTimeValue(75 * time.Second))
	packet.UpdateOption(dhcpv4.OptRebindingTimeValue(30 * time.Second))
	info = leaseToInfo(LeaseBound, lease, acquired)
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
	info := leaseToInfo(LeaseBound, lease, time.Now())
	if !info.Gateway.Equal(defaultRoute.Router) || len(info.Routes) != 2 ||
		info.Routes[1].Destination.String() != subnet.String() ||
		!info.Routes[1].Gateway.Equal(subnetRoute.Router) {
		t.Fatalf("classless routes did not override option 3: %+v", info)
	}
	packet.UpdateOption(dhcpv4.OptClasslessStaticRoute(subnetRoute))
	info = leaseToInfo(LeaseBound, lease, time.Now())
	if info.Gateway != nil || len(info.Routes) != 1 {
		t.Fatalf("option 3 supplied a default despite option 121: %+v", info)
	}
	packet.DeleteOption(dhcpv4.OptionClasslessStaticRoute)
	info = leaseToInfo(LeaseBound, lease, time.Now())
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
	rebindModifiers := append([]dhcpv4.Modifier{dhcpv4.WithBroadcast(true)}, client.requestModifiers()...)
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
			!requested.Has(dhcpv4.OptionDomainName) || !requested.Has(dhcpv4.OptionDomainNameServer) {
			t.Fatalf("request option order or standard options missing: %v", requested)
		}
	}
	if !rebind.IsBroadcast() {
		t.Fatal("rebind packet is not broadcast")
	}
}

func TestStripPrefix(t *testing.T) {
	cases := map[string]string{
		"3d06:bad:b01:ff::1/128": "3d06:bad:b01:ff::1",
		"10.0.0.1/24":            "10.0.0.1",
		"no-slash":               "no-slash",
		"":                       "",
	}
	for in, want := range cases {
		if got := StripPrefix(in); got != want {
			t.Errorf("StripPrefix(%q)=%q want %q", in, got, want)
		}
	}
}
