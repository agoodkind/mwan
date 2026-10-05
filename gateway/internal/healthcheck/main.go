// Package healthcheck provides continuous connectivity testing with structured logging.
//
// It creates new connections each iteration to exercise the full routing path.
// It tests IPv4 ping, IPv6 ping, and HTTP against diverse targets, rotating each cycle.
package healthcheck

import (
	"context"
	"log/slog"
	"net/netip"
	"os/signal"
	"syscall"
	"time"

	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/tracing"
	"goodkind.io/mwan/internal/version"
)

var (
	defaultV4Targets = []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("8.8.8.8"),
		netip.MustParseAddr("9.9.9.9"),
		netip.MustParseAddr("208.67.222.222"),
	}
	defaultV6Targets = []netip.Addr{
		netip.MustParseAddr("2606:4700:4700::1111"),
		netip.MustParseAddr("2001:4860:4860::8888"),
		netip.MustParseAddr("2620:fe::fe"),
		netip.MustParseAddr("2620:119:35::35"),
	}
	defaultHTTPSites = []string{"http://ifconfig.co/ip", "http://icanhazip.com", "http://ipv4.google.com", "http://ipv6.google.com"}
)

const (
	defaultInterval = 500 * time.Millisecond
	pingTimeout     = 2 * time.Second
	httpTimeout     = 3 * time.Second
)

// Run starts the health check loop.
func Run() error {
	interval := defaultInterval

	logger, _ := logging.New(logging.Config{
		BuildVersion: version.BuildVersionString(),
		Handlers:     []slog.Handler{logging.StdoutJSON()},
	})
	runID := tracing.NewID()
	logger = logger.With(
		slog.String(tracing.RunIDKey, runID),
		slog.String(tracing.ComponentKey, "healthcheck"),
	)
	slog.SetDefault(logger)

	slog.Info("health started", "interval", interval.String(),
		"v4_targets", len(defaultV4Targets), "v6_targets", len(defaultV6Targets), "http_sites", len(defaultHTTPSites))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	i := 0

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		v4Target := defaultV4Targets[i%len(defaultV4Targets)]
		v6Target := defaultV6Targets[i%len(defaultV6Targets)]
		httpSite := defaultHTTPSites[i%len(defaultHTTPSites)]

		_, v4Error := netif.Ping4(ctx, "", v4Target, pingTimeout)
		v4ok := v4Error == nil
		_, v6Error := netif.Ping6(ctx, "", v6Target, pingTimeout)
		v6ok := v6Error == nil
		httpCode, httpError := netif.HTTPCheck(ctx, "", httpSite, httpTimeout)
		if httpError != nil {
			httpCode = 0
		}
		httpOK := httpCode == 200

		allOK := v4ok && v6ok && httpOK

		level := slog.LevelInfo
		if !allOK {
			level = slog.LevelError
		}

		logger.Log(ctx, level, "health",
			slog.Group("v4", "target", v4Target.String(), "ok", v4ok),
			slog.Group("v6", "target", v6Target.String(), "ok", v6ok),
			slog.Group("http", "site", httpSite, "code", httpCode, "ok", httpOK),
		)

		i++
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}
