package netif

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

const (
	// RFC 4861 section 6.2.1 limits MaxRtrAdvInterval, the maximum interval between
	// unsolicited router advertisements, to 1800 s. Silence beyond this limit
	// suggests that a router stopped advertising. wakeAfterQuiet treats the next
	// advertisement with the M or O flag as evidence that a router returned.
	dhcpv6RAWakeQuiet = 1800 * time.Second
	// ReceiveRA returns at its timeout and not at context cancellation. This read
	// length bounds the time stop waits for the watcher to close its socket.
	dhcpv6RAWakeRead = time.Second
)

// Wake never blocks the caller, allowing the links module event handler to call
// it through the addresses module. The client restarts the Solicit schedule at
// SOL_TIMEOUT only while it does not have a lease.
// The client ignores the signal while it has a lease.
func (client *DHCPv6PDClient) Wake() {
	select {
	case client.wake <- struct{}{}:
	default:
	}
}

// A fresh acquisition starts at SOL_TIMEOUT and does not need an earlier wake signal.
func (client *DHCPv6PDClient) solicitWake(lease DHCPv6PDLease) <-chan struct{} {
	if len(lease.Prefixes) != 0 || len(lease.Addresses) != 0 {
		return nil
	}
	select {
	case <-client.wake:
	default:
	}
	return client.wake
}

type dhcpv6RAWake struct {
	cancel context.CancelFunc
	done   chan struct{}
}

func (client *DHCPv6PDClient) updateDHCPv6RAWake(ctx context.Context, log *slog.Logger, config DHCPv6PDConfig, lease DHCPv6PDLease, current *dhcpv6RAWake) *dhcpv6RAWake {
	if len(lease.Prefixes) != 0 || len(lease.Addresses) != 0 {
		current.stop()
		return nil
	}
	if current != nil {
		return current
	}
	watchCtx, cancel := context.WithCancel(ctx)
	watch := &dhcpv6RAWake{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(watch.done)
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(watchCtx, "dhcpv6: RA wake watcher panicked", "iface", config.Iface, "err", recovered)
			}
		}()
		client.watchDHCPv6RAWake(watchCtx, log, config)
	}()
	return watch
}

func (watch *dhcpv6RAWake) stop() {
	if watch == nil {
		return
	}
	watch.cancel()
	<-watch.done
}

// watchDHCPv6RAWake wakes the client on a router advertisement with the M or O flag
// after a quiet period. Every RAClient opens its own raw socket. The kernel delivers
// each advertisement to every raw socket on the link, and another reader on the
// link receives the same advertisements.
func (client *DHCPv6PDClient) watchDHCPv6RAWake(ctx context.Context, log *slog.Logger, config DHCPv6PDConfig) {
	// NewRAClient fails until the link has a link-local address.
	raClient, openErr := NewRAClient(config.Iface, log)
	for openErr != nil {
		log.DebugContext(ctx, "dhcpv6: RA wake watcher unavailable", "iface", config.Iface, "err", openErr)
		if !waitDHCPv6(ctx) {
			return
		}
		raClient, openErr = NewRAClient(config.Iface, log)
	}
	defer raClient.Close()
	var lastConfigured time.Time
	for ctx.Err() == nil {
		advertisement, _, err := raClient.ReceiveRA(ctx, dhcpv6RAWakeRead, false)
		if errors.Is(err, context.DeadlineExceeded) {
			continue
		}
		if err != nil {
			if ctx.Err() == nil {
				log.WarnContext(ctx, "dhcpv6: RA wake watcher read failed", "iface", config.Iface, "err", err)
			}
			return
		}
		if advertisement.ManagedConfiguration || advertisement.OtherConfiguration {
			lastConfigured = client.wakeAfterQuiet(ctx, log, config, advertisement.ManagedConfiguration, lastConfigured)
		}
	}
}

func (client *DHCPv6PDClient) wakeAfterQuiet(ctx context.Context, log *slog.Logger, config DHCPv6PDConfig, managed bool, lastConfigured time.Time) time.Time {
	now := config.Clock.Now()
	if lastConfigured.IsZero() || now.Sub(lastConfigured) > dhcpv6RAWakeQuiet {
		log.InfoContext(ctx, "dhcpv6: router advertisement requests DHCPv6 after a quiet period", "iface", config.Iface, "managed", managed, "previous", lastConfigured)
		client.Wake()
	}
	return now
}
