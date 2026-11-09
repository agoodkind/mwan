package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr/modules/health"
	"goodkind.io/mwan/internal/netif"
)

func probeWAN(
	ctx context.Context,
	wan health.WAN,
	options Options,
	logger *slog.Logger,
	records chan<- Record,
) error {
	if err := probeFamily(
		ctx,
		wan,
		wan.TargetsV6,
		"inet6",
		options.Timeout,
		options.Clock,
		logger,
		records,
	); err != nil {
		return err
	}

	return probeFamily(
		ctx,
		wan,
		wan.TargetsV4,
		"inet",
		options.Timeout,
		options.Clock,
		logger,
		records,
	)
}

func probeFamily(
	ctx context.Context,
	wan health.WAN,
	targets []netip.Addr,
	family string,
	timeout time.Duration,
	wallClock clock.Clock,
	logger *slog.Logger,
	records chan<- Record,
) error {
	if len(targets) == 0 {
		return nil
	}

	for _, target := range targets {
		for index := range wan.PingCount {
			select {
			case <-ctx.Done():
				return nil
			default:
			}

			attempt := netif.PingAttempt{
				Interface: wan.Iface,
				Family:    family,
				Target:    target.String(),
				Attempt:   index + 1,
				Timeout:   timeout,
				RTT:       0,
				Error:     "",
			}
			var err error
			if family == "inet6" {
				attempt.RTT, err = netif.Ping6WithLogger(ctx, logger, wan.Iface, target, timeout)
			} else {
				attempt.RTT, err = netif.Ping4WithLogger(ctx, logger, wan.Iface, target, timeout)
			}

			if err != nil {
				var setupError *netif.ProbeSetupError
				if errors.As(err, &setupError) {
					logger.WarnContext(
						ctx,
						"health ping setup failed",
						"wan",
						wan.Key(),
						"interface",
						wan.Iface,
						"target",
						target,
						"err",
						err,
					)
					return fmt.Errorf("ping %s via %s: %w", target, wan.Iface, err)
				}

				attempt.Error = err.Error()
			}

			records <- Record{
				WAN:       wan.WANRef,
				Timestamp: wallClock.Now().UTC(),
				Ping:      &attempt,
				HTTP:      nil,
			}
		}
	}

	for index, target := range wan.HTTPURLs {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		result, err := netif.HTTPGet(ctx, logger, wan.Iface, family, target, timeout)
		attempt := netif.HTTPAttempt{
			Interface:  wan.Iface,
			Family:     family,
			Target:     target,
			Iteration:  index + 1,
			Timeout:    timeout,
			HTTPResult: result,
			Error:      "",
		}
		if err != nil {
			var setupError *netif.ProbeSetupError
			if errors.As(err, &setupError) {
				logger.WarnContext(
					ctx,
					"health HTTP setup failed",
					"wan",
					wan.Key(),
					"interface",
					wan.Iface,
					"target",
					target,
					"err",
					err,
				)
				return fmt.Errorf("HTTP %s via %s: %w", target, wan.Iface, err)
			}

			attempt.Error = err.Error()
		}

		records <- Record{
			WAN:       wan.WANRef,
			Timestamp: wallClock.Now().UTC(),
			HTTP:      &attempt,
			Ping:      nil,
		}
	}

	return nil
}
