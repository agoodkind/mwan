package netif

import (
	"context"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv4"
)

func TestDHCPLeaseDeadlineAfterDelayedACK(t *testing.T) {
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
				synctest.Test(t, func(t *testing.T) { testDHCPLeaseDeadline(t, phase) })
			},
		)
	}
}

func testDHCPLeaseDeadline(t *testing.T, phase string) {
	t.Helper()
	const responseDelay = 1200 * time.Millisecond
	const renewal = 2 * time.Second
	const rebinding = 4 * time.Second
	const lifetime = 20 * time.Second
	address := net.IPv4(192, 0, 2, 8)
	server := net.IPv4(192, 0, 2, 1)
	requestSent := false
	requests := make(chan time.Time, 1)
	expired := phase == "expired" || phase == "expired-renew"

	reply := func(packet *dhcpv4.DHCPv4, send func(*dhcpv4.DHCPv4)) {
		if requestSent {
			return
		}

		responseType := dhcpv4.MessageTypeOffer
		responseLifetime := lifetime
		delay := time.Duration(0)
		if packet.MessageType() == dhcpv4.MessageTypeRequest {
			responseType = dhcpv4.MessageTypeAck
			renewing := !packet.ClientIPAddr.IsUnspecified()
			broadcast := packet.IsBroadcast()
			if phase == "rebind" && renewing && !broadcast {
				return
			}

			switch {
			case !renewing && (phase == "acquire" || phase == "restart" || phase == "expired"):
				delay = responseDelay
			case renewing && !broadcast && (phase == "renew" || phase == "expired-renew"):
				delay = responseDelay
			case renewing && broadcast && phase == "rebind":
				delay = responseDelay
			}
		}

		if delay != 0 {
			requestSent = true
			requests <- time.Now()
			if expired {
				responseLifetime = time.Second
			}
		}

		answer, err := dhcpv4.NewReplyFromRequest(
			packet,
			dhcpv4.WithMessageType(responseType),
			dhcpv4.WithYourIP(address),
			dhcpv4.WithOption(dhcpv4.OptServerIdentifier(server)),
			dhcpv4.WithOption(dhcpv4.OptSubnetMask(net.CIDRMask(24, 32))),
			dhcpv4.WithOption(dhcpv4.OptRenewTimeValue(renewal)),
			dhcpv4.WithOption(dhcpv4.OptRebindingTimeValue(rebinding)),
			dhcpv4.WithOption(dhcpv4.OptIPAddressLeaseTime(responseLifetime)),
		)
		if err != nil {
			t.Error(err)

			return
		}

		time.AfterFunc(delay, func() { send(answer) })
	}

	client := newDHCPTimingClient(t, time.Minute, reply)
	client.cfg.DiscoverTimeout = 5 * time.Second
	client.cfg.RequestTimeout = 5 * time.Second
	client.cfg.RenewTimeout = 5 * time.Second
	if phase == "restart" {
		client.cfg.CachedLease = &LeaseInfo{
			IP:               address,
			LinkHardwareAddr: client.origin.LinkHardwareAddr,
			ExpiresAt:        time.Now().Add(time.Minute),
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go client.run(ctx)
	deadline := time.After(lifetime)

	for {
		select {
		case event := <-client.Events:
			if event.State != LeaseBound && event.State != LeaseExpired {
				continue
			}

			var requestTime time.Time
			select {
			case requestTime = <-requests:
			default:
				continue
			}

			arrival := requestTime.Add(responseDelay)
			if !time.Now().Equal(arrival) {
				t.Fatalf(
					"received %s at %v, want ACK arrival at %v",
					event.State,
					time.Now(),
					arrival,
				)
			}

			if expired {
				if event.State != LeaseExpired || event.InvalidationEpoch != 1 {
					t.Fatalf("expired ACK published %v", event)
				}

				return
			}

			if event.State != LeaseBound || !event.IP.Equal(address) ||
				!event.AcquiredAt.Equal(arrival) ||
				!event.RenewAt.Equal(requestTime.Add(renewal)) ||
				!event.RebindAt.Equal(requestTime.Add(rebinding)) ||
				!event.ExpiresAt.Equal(requestTime.Add(lifetime)) {
				t.Fatalf(
					"assignment after delayed ACK at %v for request at %v = %+v",
					arrival,
					requestTime,
					event,
				)
			}

			return
		case <-deadline:
			t.Fatalf(
				"delayed %s ACK did not produce the expected assignment: %v",
				phase,
				client.LastLease(),
			)
		}
	}
}
