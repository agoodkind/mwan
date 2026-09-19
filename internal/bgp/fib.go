package bgp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

// FIBConfig identifies the kernel routing tables owned by the BGP installer.
type FIBConfig struct {
	Tables        []int
	InternalIface string
}

// PathEvent is the accepted best-path change received from a BGP peer.
type PathEvent struct {
	Peer      string
	Prefix    netip.Prefix
	NextHop   netip.Addr
	Withdrawn bool
}

type routeWriter interface {
	ReconcileTableRoute(context.Context, *slog.Logger, netif.RouteSpec) error
	DeleteTableRoute(context.Context, *slog.Logger, netif.RouteSpec) error
	ListProtocolRoutes(context.Context, *slog.Logger, string, int, int) ([]netif.CurrentRoute, error)
}

type netifRouteWriter struct{}

func (netifRouteWriter) ReconcileTableRoute(
	ctx context.Context,
	log *slog.Logger,
	route netif.RouteSpec,
) error {
	if err := netif.ReconcileTableRoute(ctx, log, route); err != nil {
		log.ErrorContext(ctx, "reconcile BGP table route", "err", err, "route", route)
		return fmt.Errorf("reconcile BGP table route: %w", err)
	}
	return nil
}

func (netifRouteWriter) DeleteTableRoute(
	ctx context.Context,
	log *slog.Logger,
	route netif.RouteSpec,
) error {
	if err := netif.DeleteTableRoute(ctx, log, route); err != nil {
		log.ErrorContext(ctx, "delete BGP table route", "err", err, "route", route)
		return fmt.Errorf("delete BGP table route: %w", err)
	}
	return nil
}

func (netifRouteWriter) ListProtocolRoutes(
	ctx context.Context,
	log *slog.Logger,
	family string,
	tableID int,
	protocol int,
) ([]netif.CurrentRoute, error) {
	routes, err := netif.ListProtocolRoutes(ctx, log, family, tableID, protocol)
	if err != nil {
		log.ErrorContext(ctx, "list BGP table routes", "err", err, "table_id", tableID)
		return nil, fmt.Errorf("list BGP table routes: %w", err)
	}
	return routes, nil
}

// FIB reconciles accepted BGP paths into the owned kernel routing tables.
type FIB struct {
	cfg     FIBConfig
	log     *slog.Logger
	writer  routeWriter
	mu      sync.Mutex
	desired map[string]desiredRoute
	sweep   sync.Once
	armed   bool
}

type desiredRoute struct {
	peer    string
	nextHop netip.Addr
}

// NewFIB creates a learned-route installer.
func NewFIB(cfg FIBConfig, log *slog.Logger) *FIB {
	return newFIB(cfg, log, netifRouteWriter{})
}

func newFIB(cfg FIBConfig, log *slog.Logger, writer routeWriter) *FIB {
	if log == nil {
		log = slog.Default()
	}
	cfg.Tables = append([]int(nil), cfg.Tables...)
	return &FIB{
		cfg:     cfg,
		log:     log,
		writer:  writer,
		mu:      sync.Mutex{},
		desired: make(map[string]desiredRoute),
		sweep:   sync.Once{},
		armed:   false,
	}
}

// ArmSweep permits stale-route cleanup after a dynamic session proves recovery
// or the no-peer fallback expires. Earlier sweeping could delete retained live
// routes before their sessions repopulate desired state.
func (f *FIB) ArmSweep() {
	f.sweep.Do(func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.armed = true
	})
}

// Apply installs or withdraws one accepted best path in every owned table.
func (f *FIB) Apply(ctx context.Context, event PathEvent) error {
	prefix, err := pathPrefix(event.Prefix)
	if err != nil {
		return err
	}
	if !event.Withdrawn && !event.NextHop.IsValid() {
		return fmt.Errorf("BGP path %s has an invalid next hop", prefix)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if event.Withdrawn {
		return f.withdraw(ctx, event.Peer, prefix)
	}

	f.desired[prefix.String()] = desiredRoute{peer: event.Peer, nextHop: event.NextHop}
	return f.installPrefix(ctx, prefix, event.NextHop)
}

// SweepStale removes owned BGP routes absent from accepted best paths only after
// ArmSweep. The recovery gate lets dynamic sessions repopulate retained routes
// before cleanup runs.
func (f *FIB) SweepStale(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.armed {
		return nil
	}

	var sweepErr error
	for _, tableID := range f.cfg.Tables {
		routes, err := f.writer.ListProtocolRoutes(
			ctx,
			f.log,
			"inet6",
			tableID,
			unix.RTPROT_BGP,
		)
		if err != nil {
			sweepErr = errors.Join(sweepErr, fmt.Errorf("list table %d BGP routes: %w", tableID, err))
			continue
		}

		for _, route := range routes {
			if _, ok := f.desired[route.Dest]; ok {
				continue
			}
			if err := f.deleteRoute(ctx, "", tableID, route); err != nil {
				sweepErr = errors.Join(sweepErr, err)
			}
		}
	}
	return sweepErr
}

func (f *FIB) withdraw(ctx context.Context, peer string, prefix netip.Prefix) error {
	matched := make([]netip.Prefix, 0)
	for desiredText, desired := range f.desired {
		if desired.peer != peer {
			continue
		}
		desiredPrefix, err := netip.ParsePrefix(desiredText)
		if err != nil {
			continue
		}
		if !prefix.Contains(desiredPrefix.Addr()) || desiredPrefix.Bits() < prefix.Bits() {
			continue
		}
		matched = append(matched, desiredPrefix)
	}
	if len(matched) == 0 {
		return f.deletePrefix(ctx, peer, prefix)
	}

	var withdrawErr error
	for _, matchedPrefix := range matched {
		delete(f.desired, matchedPrefix.String())
		if err := f.deletePrefix(ctx, peer, matchedPrefix); err != nil {
			withdrawErr = errors.Join(withdrawErr, err)
		}
	}
	return withdrawErr
}

// WithdrawPeer removes every desired route that the disconnected peer announced.
func (f *FIB) WithdrawPeer(ctx context.Context, peer string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	var withdrawErr error
	for prefixText, desired := range f.desired {
		if desired.peer != peer {
			continue
		}
		prefix, err := netip.ParsePrefix(prefixText)
		if err != nil {
			continue
		}
		delete(f.desired, prefixText)
		if err := f.deletePrefix(ctx, peer, prefix); err != nil {
			withdrawErr = errors.Join(withdrawErr, err)
		}
	}
	return withdrawErr
}

func pathPrefix(prefix netip.Prefix) (netip.Prefix, error) {
	if !prefix.IsValid() || !prefix.Addr().Is6() {
		return netip.Prefix{}, fmt.Errorf("BGP path prefix %s is not a valid IPv6 prefix", prefix)
	}
	return prefix.Masked(), nil
}

func (f *FIB) installPrefix(
	ctx context.Context,
	prefix netip.Prefix,
	nextHop netip.Addr,
) error {
	var installErr error
	for _, tableID := range f.cfg.Tables {
		route := f.route(prefix, nextHop, tableID)
		if err := f.writer.ReconcileTableRoute(ctx, f.log, route); err != nil {
			installErr = errors.Join(installErr, fmt.Errorf("install table %d route %s: %w", tableID, prefix, err))
		}
	}
	return installErr
}

func (f *FIB) deletePrefix(ctx context.Context, peer string, prefix netip.Prefix) error {
	var deleteErr error
	for _, tableID := range f.cfg.Tables {
		route := f.route(prefix, netip.Addr{}, tableID)
		current := netif.CurrentRoute{
			Dest:   route.Dest,
			Via:    route.Via,
			Dev:    route.Dev,
			Metric: route.Metric,
		}
		if err := f.deleteRoute(ctx, peer, tableID, current); err != nil {
			deleteErr = errors.Join(deleteErr, err)
		}
	}
	return deleteErr
}

func (f *FIB) deleteRoute(
	ctx context.Context,
	peer string,
	tableID int,
	current netif.CurrentRoute,
) error {
	route := netif.RouteSpec{
		Family:   "inet6",
		Dest:     current.Dest,
		Via:      current.Via,
		Dev:      current.Dev,
		TableID:  tableID,
		Metric:   current.Metric,
		Protocol: unix.RTPROT_BGP,
	}
	if err := f.writer.DeleteTableRoute(ctx, f.log, route); err != nil {
		f.log.ErrorContext(ctx, "delete BGP FIB route failed", "err", err, "peer", peer, "route", route)
		return fmt.Errorf("delete table %d route %s: %w", tableID, route.Dest, err)
	}
	return nil
}

func (f *FIB) route(prefix netip.Prefix, nextHop netip.Addr, tableID int) netif.RouteSpec {
	via := ""
	if nextHop.IsValid() {
		via = nextHop.String()
	}
	return netif.RouteSpec{
		Family:   "inet6",
		Dest:     prefix.String(),
		Via:      via,
		Dev:      f.cfg.InternalIface,
		TableID:  tableID,
		Metric:   0,
		Protocol: unix.RTPROT_BGP,
	}
}
