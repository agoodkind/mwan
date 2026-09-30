package netif

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"time"

	"github.com/insomniacslk/dhcp/dhcpv6"
	"github.com/insomniacslk/dhcp/dhcpv6/nclient6"
)

type dhcpv6RestartExchangeError struct {
	operation string
	cause     error
}

func (err *dhcpv6RestartExchangeError) Error() string {
	return err.operation + ": " + err.cause.Error()
}

func (err *dhcpv6RestartExchangeError) Unwrap() error {
	return err.cause
}

func (client *DHCPv6PDClient) recoverOnLink(ctx context.Context, log *slog.Logger, config DHCPv6PDConfig, link *net.Interface) (DHCPv6PDLease, time.Duration, error) {
	var empty DHCPv6PDLease
	solicitMaxRT := time.Hour
	if config.CachedLease == nil || !compatibleDHCPv6Lease(config, link, *config.CachedLease) {
		return empty, solicitMaxRT, nil
	}
	transport, err := nclient6.New(config.Iface, nclient6.WithRetry(1), nclient6.WithTimeout(5*time.Second))
	if err != nil {
		return empty, solicitMaxRT, fmt.Errorf("open DHCPv6 recovery socket on %s: %w", config.Iface, err)
	}
	lease := client.recoverCachedDHCPv6(ctx, log, transport, config, link, &solicitMaxRT)
	if err := transport.Close(); err != nil {
		log.WarnContext(ctx, "dhcpv6: close recovery socket failed", "err", err)
	}
	return lease, solicitMaxRT, nil
}

func exchangeDHCPv6Restart(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, messageType dhcpv6.MessageType, cached DHCPv6PDLease, solicitMaxRT *time.Duration) (*dhcpv6.Message, error) {
	duid, err := dhcpv6.DUIDFromBytes(config.DUID)
	if err != nil {
		return nil, &dhcpv6RestartExchangeError{operation: "decode DHCPv6 DUID", cause: err}
	}
	initial, err := dhcpv6.NewMessage(dhcpv6.WithClientID(duid))
	if err != nil {
		return nil, &dhcpv6RestartExchangeError{operation: "create DHCPv6 restart message", cause: err}
	}
	delay, err := rand.Int(rand.Reader, big.NewInt(int64(time.Second)))
	if err != nil {
		return nil, &dhcpv6RestartExchangeError{operation: "choose DHCPv6 restart delay", cause: err}
	}
	if !waitDHCPv6Duration(ctx, time.Duration(delay.Int64())) {
		return nil, &dhcpv6RestartExchangeError{operation: "wait before DHCPv6 restart", cause: ctx.Err()}
	}
	started := config.Clock.Now()
	deadline := started.Add(10 * time.Second)
	rt := jitterDHCPv6(time.Second)
	for ctx.Err() == nil && config.Clock.Now().Before(deadline) {
		current := expireDHCPv6Assignments(cached, config.Clock.Now())
		if len(current.Prefixes) == 0 && len(current.Addresses) == 0 {
			return nil, errDHCPv6RestartRejected
		}
		message, err := dhcpv6.NewMessage(dhcpv6.WithClientID(duid))
		if err != nil {
			return nil, &dhcpv6RestartExchangeError{operation: "create DHCPv6 restart retransmission", cause: err}
		}
		message.TransactionID = initial.TransactionID
		configureDHCPv6Message(message, config, messageType, &current)
		message.UpdateOption(dhcpv6.OptElapsedTime(config.Clock.Now().Sub(started)))
		wait := min(rt, deadline.Sub(config.Clock.Now()), latestDHCPv6Validity(current).Sub(config.Clock.Now()))
		if wait <= 0 {
			break
		}
		attemptStarted := config.Clock.Now()
		attemptCtx, cancel := context.WithTimeout(ctx, wait)
		response, exchangeErr := transport.SendAndRead(attemptCtx, transport.RemoteAddr(), message, func(response *dhcpv6.Message) bool {
			if response.MessageType != dhcpv6.MessageTypeReply {
				return false
			}
			client := response.Options.ClientID()
			server := response.Options.ServerID()
			return client != nil && server != nil && bytes.Equal(client.ToBytes(), config.DUID)
		})
		cancel()
		if exchangeErr == nil {
			updateDHCPv6SolMaxRT(response, solicitMaxRT)
			return response, nil
		}
		if ctx.Err() != nil {
			return nil, &dhcpv6RestartExchangeError{operation: "DHCPv6 restart interrupted", cause: ctx.Err()}
		}
		if !waitDHCPv6Duration(ctx, attemptStarted.Add(wait).Sub(config.Clock.Now())) {
			return nil, &dhcpv6RestartExchangeError{operation: "DHCPv6 restart wait interrupted", cause: ctx.Err()}
		}
		rt = nextDHCPv6RestartRT(rt)
	}
	if ctx.Err() != nil {
		return nil, &dhcpv6RestartExchangeError{operation: "DHCPv6 restart interrupted", cause: ctx.Err()}
	}
	return nil, context.DeadlineExceeded
}

func nextDHCPv6RestartRT(previous time.Duration) time.Duration {
	next := previous + jitterDHCPv6(previous)
	if next > 4*time.Second {
		return jitterDHCPv6(4 * time.Second)
	}
	return next
}

func waitDHCPv6Duration(ctx context.Context, duration time.Duration) bool {
	if duration <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
