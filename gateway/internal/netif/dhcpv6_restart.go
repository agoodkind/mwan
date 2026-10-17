package netif

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
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

// RecoveryDone closes after initial cached lease validation or client shutdown.
func (client *DHCPv6PDClient) RecoveryDone() <-chan struct{} {
	return client.recoveryDone
}

func (client *DHCPv6PDClient) completeRecovery() {
	client.recoveryOnce.Do(func() { close(client.recoveryDone) })
}

func (client *DHCPv6PDClient) recoverCachedDHCPv6(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, link *net.Interface, solicitMaxRT *time.Duration) DHCPv6PDLease {
	if config.CachedLease == nil || !compatibleDHCPv6Lease(config, link, *config.CachedLease) {
		var empty DHCPv6PDLease
		return empty
	}
	for ctx.Err() == nil {
		cached := expireDHCPv6Assignments(*config.CachedLease, config.Clock.Now())
		if len(cached.Prefixes) == 0 && len(cached.Addresses) == 0 {
			break
		}
		lease, err := validateDHCPv6Restart(ctx, log, transport, config, link, cached, solicitMaxRT)
		if err == nil {
			client.publish(lease)
			return lease
		}
		if errors.Is(err, errDHCPv6RestartRejected) || ctx.Err() != nil {
			break
		}
		log.WarnContext(ctx, "dhcpv6: restart validation pending", "iface", config.Iface, "err", err)
		if !waitDHCPv6(ctx) {
			break
		}
	}
	var empty DHCPv6PDLease
	return empty
}

func (client *DHCPv6PDClient) recoverOnLink(ctx context.Context, log *slog.Logger, config DHCPv6PDConfig, link *net.Interface) (DHCPv6PDLease, time.Duration, error) {
	var empty DHCPv6PDLease
	solicitMaxRT := time.Hour
	if config.CachedLease == nil || !compatibleDHCPv6Lease(config, link, *config.CachedLease) {
		return empty, solicitMaxRT, nil
	}
	transport, err := nclient6.New(config.Iface, dhcpv6TransportOptions()...)
	if err != nil {
		return empty, solicitMaxRT, fmt.Errorf("open DHCPv6 recovery socket on %s: %w", config.Iface, err)
	}
	lease := client.recoverCachedDHCPv6(ctx, log, transport, config, link, &solicitMaxRT)
	if err := transport.Close(); err != nil {
		log.WarnContext(ctx, "dhcpv6: close recovery socket failed", "err", err)
	}
	return lease, solicitMaxRT, nil
}

func exchangeDHCPv6Restart(ctx context.Context, log *slog.Logger, transport *nclient6.Client, config DHCPv6PDConfig, messageType dhcpv6.MessageType, cached DHCPv6PDLease, solicitMaxRT *time.Duration) (*dhcpv6.Message, error) {
	timing := dhcpv6Retransmission{
		maxDelay: dhcpv6ConfirmMaxDelay, initial: dhcpv6ConfirmTimeout, maximum: dhcpv6ConfirmMaxRT,
		count: 0, duration: dhcpv6ConfirmMaxDuration, end: latestDHCPv6Validity(cached),
	}
	prepare := func(message *dhcpv6.Message) error {
		current := expireDHCPv6Assignments(cached, config.Clock.Now())
		if len(current.Prefixes) == 0 && len(current.Addresses) == 0 {
			return errDHCPv6RestartRejected
		}
		if messageType == dhcpv6.MessageTypeRebind && len(current.Prefixes) == 0 {
			return context.DeadlineExceeded
		}
		requestConfig := config
		requestConfig.RequestPrefix = len(current.Prefixes) != 0
		requestConfig.RequestAddress = len(current.Addresses) != 0
		configureDHCPv6Message(message, requestConfig, messageType, &current)
		return nil
	}
	match := func(response *dhcpv6.Message) bool {
		if response.MessageType != dhcpv6.MessageTypeReply {
			return false
		}
		client := response.Options.ClientID()
		server := response.Options.ServerID()
		return client != nil && server != nil && bytes.Equal(client.ToBytes(), config.DUID)
	}
	response, err := exchangeDHCPv6Retransmitting(ctx, log, transport, config, messageType, timing, prepare, match, nil)
	if err != nil {
		return nil, &dhcpv6RestartExchangeError{operation: "DHCPv6 restart " + messageType.String(), cause: err}
	}
	updateDHCPv6SolMaxRT(response, solicitMaxRT)
	return response, nil
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
