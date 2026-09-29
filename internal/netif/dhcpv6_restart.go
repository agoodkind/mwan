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

func (client *DHCPv6PDClient) recoverOnLink(ctx context.Context, log *slog.Logger, config DHCPv6PDConfig, link *net.Interface) (DHCPv6PDLease, time.Duration, bool) {
	var empty DHCPv6PDLease
	solicitMaxRT := time.Hour
	if config.CachedLease == nil || !compatibleDHCPv6Lease(config, link, *config.CachedLease) {
		return empty, solicitMaxRT, true
	}
	transport, err := nclient6.New(config.Iface, nclient6.WithRetry(1), nclient6.WithTimeout(5*time.Second))
	if err != nil {
		log.WarnContext(ctx, "dhcpv6: recovery socket unavailable", "iface", config.Iface, "err", err)
		return empty, solicitMaxRT, false
	}
	lease := client.recoverCachedDHCPv6(ctx, log, transport, config, link, &solicitMaxRT)
	if err := transport.Close(); err != nil {
		log.WarnContext(ctx, "dhcpv6: close recovery socket failed", "err", err)
	}
	return lease, solicitMaxRT, true
}

func exchangeDHCPv6Restart(ctx context.Context, transport *nclient6.Client, config DHCPv6PDConfig, messageType dhcpv6.MessageType, cached DHCPv6PDLease, solicitMaxRT *time.Duration) (*dhcpv6.Message, error) {
	duid, err := dhcpv6.DUIDFromBytes(config.DUID)
	if err != nil {
		slog.WarnContext(ctx, "dhcpv6: invalid restart DUID", "err", err)
		return nil, fmt.Errorf("decode DHCPv6 DUID: %w", err)
	}
	initial, err := dhcpv6.NewMessage(dhcpv6.WithClientID(duid))
	if err != nil {
		slog.WarnContext(ctx, "dhcpv6: restart message creation failed", "err", err)
		return nil, fmt.Errorf("create DHCPv6 restart message: %w", err)
	}
	delay, err := rand.Int(rand.Reader, big.NewInt(int64(time.Second)))
	if err != nil {
		slog.WarnContext(ctx, "dhcpv6: restart delay selection failed", "err", err)
		return nil, fmt.Errorf("choose DHCPv6 restart delay: %w", err)
	}
	if !waitDHCPv6Duration(ctx, time.Duration(delay.Int64())) {
		return nil, fmt.Errorf("wait before DHCPv6 restart: %w", ctx.Err())
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
			slog.WarnContext(ctx, "dhcpv6: restart retransmission creation failed", "err", err)
			return nil, fmt.Errorf("create DHCPv6 restart retransmission: %w", err)
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
			return nil, fmt.Errorf("DHCPv6 restart interrupted: %w", ctx.Err())
		}
		if !waitDHCPv6Duration(ctx, attemptStarted.Add(wait).Sub(config.Clock.Now())) {
			return nil, fmt.Errorf("DHCPv6 restart wait interrupted: %w", ctx.Err())
		}
		rt = nextDHCPv6RestartRT(rt)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("DHCPv6 restart interrupted: %w", ctx.Err())
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
