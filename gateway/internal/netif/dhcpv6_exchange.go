package netif

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/netip"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
)

// RFC 8415 section 7.6 transmission and retransmission parameters.
const (
	dhcpv6SolicitMaxDelay    = time.Second
	dhcpv6SolicitTimeout     = time.Second
	dhcpv6RequestTimeout     = time.Second
	dhcpv6RequestMaxRT       = 30 * time.Second
	dhcpv6RequestMaxCount    = 10
	dhcpv6ConfirmMaxDelay    = time.Second
	dhcpv6ConfirmTimeout     = time.Second
	dhcpv6ConfirmMaxRT       = 4 * time.Second
	dhcpv6ConfirmMaxDuration = 10 * time.Second
	dhcpv6RenewTimeout       = 10 * time.Second
	dhcpv6RenewMaxRT         = 600 * time.Second
	dhcpv6RebindTimeout      = 10 * time.Second
	dhcpv6RebindMaxRT        = 600 * time.Second
	dhcpv6DeclineTimeout     = time.Second
	// RFC 8415 section 21.9 caps Elapsed Time at 0xffff hundredths of a second.
	dhcpv6MaxElapsedTime = 0xffff * 10 * time.Millisecond
	// nclient6 ends a send at its own timeout. This limit exceeds the largest
	// SOL_MAX_RT a server may set, and each transmission context ends first.
	dhcpv6TransportTimeout = 48 * time.Hour
)

// errDHCPv6ExchangeTimeout reports an exchange that ended without a matching reply.
var errDHCPv6ExchangeTimeout = errors.New("DHCPv6 exchange ended without a reply")

// errDHCPv6ExchangeInterrupted reports an exchange that ended with its context.
var errDHCPv6ExchangeInterrupted = errors.New("DHCPv6 exchange interrupted")

// errDHCPv6ExchangeAbandoned reports an exchange that lost every assignment it renewed.
var errDHCPv6ExchangeAbandoned = errors.New("DHCPv6 exchange has no remaining assignment")

// dhcpv6Retransmission stores the RFC 8415 section 15 parameters of one exchange.
// A zero count, duration, or end leaves that limit unset.
type dhcpv6Retransmission struct {
	maxDelay time.Duration
	initial  time.Duration
	maximum  time.Duration
	count    int
	duration time.Duration
	end      time.Time
}

// dhcpv6ExchangeService runs lease work that must not wait for a long exchange to end.
// Each callback reports whether any assignment remains for the exchange to renew.
type dhcpv6ExchangeService struct {
	next     func() time.Time
	expire   func(ctx context.Context) bool
	declines <-chan netip.Addr
	decline  func(ctx context.Context, address netip.Addr) bool
}

func dhcpv6TransportOptions() []nclient6.ClientOpt {
	return []nclient6.ClientOpt{nclient6.WithRetry(1), nclient6.WithTimeout(dhcpv6TransportTimeout)}
}

// exchangeDHCPv6Retransmitting uses one transaction ID for every transmission of the
// exchange. A late reply to an earlier transmission matches that ID and completes it.
func exchangeDHCPv6Retransmitting(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, messageType dhcpv6.MessageType, timing dhcpv6Retransmission, prepare func(*dhcpv6.Message) error, match nclient6.Matcher, service *dhcpv6ExchangeService) (*dhcpv6.Message, error) {
	duid, err := dhcpv6.DUIDFromBytes(config.DUID)
	if err != nil {
		log.WarnContext(ctx, "dhcpv6: invalid configured DUID", "iface", config.Iface, "err", err)
		return nil, fmt.Errorf("decode DHCPv6 DUID: %w", err)
	}
	transactionID, err := dhcpv6.GenerateTransactionID()
	if err != nil {
		log.WarnContext(ctx, "dhcpv6: transaction ID unavailable", "iface", config.Iface, "message", messageType, "err", err)
		return nil, fmt.Errorf("create DHCPv6 %s transaction ID: %w", messageType, err)
	}
	if timing.maxDelay > 0 {
		delay, err := rand.Int(rand.Reader, big.NewInt(int64(timing.maxDelay)))
		if err != nil {
			log.WarnContext(ctx, "dhcpv6: initial delay unavailable", "iface", config.Iface, "message", messageType, "err", err)
			return nil, fmt.Errorf("choose DHCPv6 %s delay: %w", messageType, err)
		}
		if !waitDHCPv6Duration(ctx, time.Duration(delay.Int64())) {
			return nil, errDHCPv6ExchangeInterrupted
		}
	}
	started := config.Clock.Now()
	end := timing.end
	if timing.duration > 0 {
		end = earliestDHCPv6Time(end, started.Add(timing.duration))
	}
	rt := firstDHCPv6RT(messageType, timing.initial)
	var response *dhcpv6.Message
	var sent time.Time
	transmission := 0
	for response == nil && (timing.count == 0 || transmission < timing.count) {
		sent = config.Clock.Now()
		if !end.IsZero() && !sent.Before(end) {
			break
		}
		transmission++
		message := &dhcpv6.Message{MessageType: messageType, TransactionID: transactionID, Options: dhcpv6.MessageOptions{Options: dhcpv6.Options{}}}
		dhcpv6.WithClientID(duid)(message)
		if err := prepare(message); err != nil {
			return nil, err
		}
		message.UpdateOption(dhcpv6.OptElapsedTime(min(sent.Sub(started), dhcpv6MaxElapsedTime)))
		if transmission > 1 {
			log.DebugContext(ctx, "dhcpv6: retransmitting", "iface", config.Iface, "message", messageType, "xid", transactionID, "transmission", transmission, "rt", rt, "elapsed", sent.Sub(started))
		}
		response, err = transmitDHCPv6(ctx, log, transport, config, message, match, earliestDHCPv6Time(end, sent.Add(rt)), service)
		if err != nil && !errors.Is(err, errDHCPv6ExchangeTimeout) {
			return nil, err
		}
		rt = nextDHCPv6RT(rt, timing.maximum)
	}
	if response == nil {
		log.WarnContext(ctx, "dhcpv6: exchange ended without a reply", "iface", config.Iface, "message", messageType, "xid", transactionID, "transmissions", transmission, "elapsed", config.Clock.Now().Sub(started))
		return nil, fmt.Errorf("DHCPv6 %s xid %s: %w", messageType, transactionID, errDHCPv6ExchangeTimeout)
	}
	replied := config.Clock.Now()
	log.InfoContext(ctx, "dhcpv6: exchange completed", "iface", config.Iface, "message", messageType, "xid", transactionID, "transmissions", transmission, "reply_delay", replied.Sub(sent), "elapsed", replied.Sub(started))
	return response, nil
}

// firstDHCPv6RT follows RFC 8415 section 15, where the first Solicit RT must exceed IRT.
func firstDHCPv6RT(messageType dhcpv6.MessageType, initial time.Duration) time.Duration {
	if messageType != dhcpv6.MessageTypeSolicit {
		return jitterDHCPv6(initial)
	}
	extra, err := rand.Int(rand.Reader, big.NewInt(int64(initial/10)))
	if err != nil {
		return initial + initial/10
	}
	return initial + time.Duration(extra.Int64()) + 1
}

func nextDHCPv6RT(previous, maximum time.Duration) time.Duration {
	next := previous + jitterDHCPv6(previous)
	if maximum > 0 && next > maximum {
		return jitterDHCPv6(maximum)
	}
	return next
}

func earliestDHCPv6Time(current, candidate time.Time) time.Time {
	if current.IsZero() || candidate.Before(current) {
		return candidate
	}
	return current
}
