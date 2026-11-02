package healthcheck

import (
	"context"
	"log/slog"
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr/modules/health"
	"goodkind.io/mwan/internal/netif"
)

func probeWAN(ctx context.Context, wan health.WAN, options Options, logger *slog.Logger, records chan<- Record) {
	probeFamily(ctx, wan, wan.TargetsV6, "inet6", options.Timeout, options.Clock, logger, records)
	probeFamily(ctx, wan, wan.TargetsV4, "inet", options.Timeout, options.Clock, logger, records)
}

func probeFamily(ctx context.Context, wan health.WAN, targets []netip.Addr, family string, timeout time.Duration, wallClock clock.Clock, logger *slog.Logger, records chan<- Record) {
	if len(targets) == 0 {
		return
	}
	for _, target := range targets {
		for index := range wan.PingCount {
			if ctx.Err() != nil {
				return
			}
			attempt := netif.PingAttempt{Interface: wan.Iface, Family: family, Target: target.String(), Attempt: index + 1, Timeout: timeout, RTT: 0, Error: ""}
			var err error
			if family == "inet6" {
				attempt.RTT, err = netif.Ping6WithLogger(ctx, logger, wan.Iface, target, timeout)
			} else {
				attempt.RTT, err = netif.Ping4WithLogger(ctx, logger, wan.Iface, target, timeout)
			}
			if err != nil {
				attempt.Error = err.Error()
			}
			records <- Record{WAN: wan.WANRef, Timestamp: wallClock.Now().UTC(), Ping: &attempt, HTTP: nil}
		}
	}
	for index, target := range wan.HTTPURLs {
		if ctx.Err() != nil {
			return
		}
		result, err := netif.HTTPGet(ctx, logger, wan.Iface, family, target, timeout)
		attempt := netif.HTTPAttempt{Interface: wan.Iface, Family: family, Target: target, Iteration: index + 1, Timeout: timeout, HTTPResult: result, Error: ""}
		if err != nil {
			attempt.Error = err.Error()
		}
		records <- Record{WAN: wan.WANRef, Timestamp: wallClock.Now().UTC(), HTTP: &attempt, Ping: nil}
	}
}
