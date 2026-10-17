package netif

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
)

type dhcpv6TransmissionResult struct {
	response *dhcpv6.Message
	err      error
}

// transmitDHCPv6 sends one transmission and listens until the next one is due.
// nclient6 matches a reply only during SendAndRead. SendAndRead runs in its own
// goroutine, and this goroutine runs the service work.
func transmitDHCPv6(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, message *dhcpv6.Message, match nclient6.Matcher, until time.Time, service *dhcpv6ExchangeService) (*dhcpv6.Message, error) {
	attemptCtx, cancel := context.WithDeadline(ctx, until)
	defer cancel()
	results := make(chan dhcpv6TransmissionResult, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(ctx, "dhcpv6: receive panicked", "iface", config.Iface, "xid", message.TransactionID, "err", recovered)
				results <- dhcpv6TransmissionResult{response: nil, err: fmt.Errorf("DHCPv6 receive panicked: %v", recovered)}
			}
		}()
		response, err := transport.SendAndRead(attemptCtx, transport.RemoteAddr(), message, match)
		results <- dhcpv6TransmissionResult{response: response, err: err}
	}()
	abandoned := false
	for {
		var dueTimer *time.Timer
		var due <-chan time.Time
		var declines <-chan netip.Addr
		if service != nil && !abandoned {
			declines = service.declines
			if next := service.next(); !next.IsZero() {
				dueTimer = time.NewTimer(max(next.Sub(config.Clock.Now()), 0))
				due = dueTimer.C
			}
		}
		// A failed send counts as a transmission. The wait ends at until, which is the
		// time of the next scheduled retransmission or the end of the exchange.
		var listening <-chan struct{}
		if results == nil {
			listening = attemptCtx.Done()
		}
		select {
		case result := <-results:
			results = nil
			if abandoned {
				stopDHCPv6Timer(dueTimer)
				return nil, endedDHCPv6Transmission(ctx, abandoned)
			}
			if result.err == nil {
				stopDHCPv6Timer(dueTimer)
				return result.response, nil
			}
			if attemptCtx.Err() != nil {
				stopDHCPv6Timer(dueTimer)
				return nil, endedDHCPv6Transmission(ctx, abandoned)
			}
			log.WarnContext(ctx, "dhcpv6: transmission failed", "iface", config.Iface, "message", message.MessageType, "xid", message.TransactionID, "err", result.err)
		case <-listening:
			stopDHCPv6Timer(dueTimer)
			return nil, endedDHCPv6Transmission(ctx, abandoned)
		case <-due:
			if !service.expire(ctx) {
				abandoned = true
				cancel()
			}
		case address := <-declines:
			if !service.decline(ctx, address) {
				abandoned = true
				cancel()
			}
		}
		stopDHCPv6Timer(dueTimer)
	}
}

func endedDHCPv6Transmission(ctx context.Context, abandoned bool) error {
	if ctx.Err() != nil {
		return errDHCPv6ExchangeInterrupted
	}
	if abandoned {
		return errDHCPv6ExchangeAbandoned
	}
	return errDHCPv6ExchangeTimeout
}

func stopDHCPv6Timer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}
