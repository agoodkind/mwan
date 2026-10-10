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
// Use different metrics for installers that share a table because a kernel
// route replacement identifies an IPv6 route by destination and metric. A zero
// Metric selects the kernel default metric.
type FIBConfig struct {
	Tables        []int
	InternalIface string
	Metric        uint32
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

const (
	listedDefaultRoute = "default"
	ipv4DefaultPrefix  = "0.0.0.0/0"
	ipv6DefaultPrefix  = "::/0"
	ipv4RouteFamily    = "inet"
	ipv6RouteFamily    = "inet6"
)

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
		for _, family := range [...]string{ipv4RouteFamily, ipv6RouteFamily} {
			if err := f.sweepFamily(ctx, tableID, family); err != nil {
				sweepErr = errors.Join(sweepErr, err)
			}
		}
	}
	return sweepErr
}

func (f *FIB) sweepFamily(ctx context.Context, tableID int, family string) error {
	routes, err := f.writer.ListProtocolRoutes(
		ctx,
		f.log,
		family,
		tableID,
		unix.RTPROT_BGP,
	)
	if err != nil {
		f.log.ErrorContext(ctx, "list BGP table routes", "err", err, "table_id", tableID)
		return fmt.Errorf("list table %d BGP routes: %w", tableID, err)
	}

	var sweepErr error
	for _, route := range routes {
		// Another installer can write BGP routes for a different device
		// in the same table.
		if route.Dev != f.cfg.InternalIface {
			continue
		}
		// Kernel route listings use default for each address family's default route
		if route.Dest == listedDefaultRoute {
			route.Dest = defaultPrefix(family)
		}
		if _, ok := f.desired[route.Dest]; ok {
			continue
		}
		if err := f.deleteRoute(ctx, "", tableID, family, route); err != nil {
			sweepErr = errors.Join(sweepErr, err)
		}
	}
	return sweepErr
}

func (f *FIB) coversConnected(ctx context.Context, prefix netip.Prefix) (bool, error) {
	masked, err := pathPrefix(prefix)
	if err != nil {
		return false, err
	}
	routes, err := f.writer.ListProtocolRoutes(
		ctx,
		f.log,
		prefixFamily(masked),
		unix.RT_TABLE_MAIN,
		unix.RTPROT_KERNEL,
	)
	if err != nil {
		f.log.ErrorContext(ctx, "list main table kernel routes failed", "err", err, "prefix", masked)
		return false, fmt.Errorf("list main table kernel routes: %w", err)
	}
	for _, route := range routes {
		if route.Dev != f.cfg.InternalIface {
			continue
		}
		connected, err := netip.ParsePrefix(route.Dest)
		if err != nil {
			continue
		}
		if masked.Bits() <= connected.Bits() && masked.Contains(connected.Addr()) {
			return true, nil
		}
	}
	return false, nil
}

func prefixFamily(prefix netip.Prefix) string {
	if prefix.Addr().Is4() {
		return ipv4RouteFamily
	}
	return ipv6RouteFamily
}

func defaultPrefix(family string) string {
	if family == ipv4RouteFamily {
		return ipv4DefaultPrefix
	}
	return ipv6DefaultPrefix
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

// WithdrawExact preserves the peer's more specific routes inside prefix when
// removing the desired route for prefix. A withdrawal through Apply removes the
// peer's more specific routes inside prefix.
func (f *FIB) WithdrawExact(ctx context.Context, peer string, prefix netip.Prefix) error {
	masked, err := pathPrefix(prefix)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.desired, masked.String())
	return f.deletePrefix(ctx, peer, masked)
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
	if !prefix.IsValid() {
		return netip.Prefix{}, fmt.Errorf("invalid BGP path prefix: %s", prefix)
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
			Family:   route.Family,
			Dest:     route.Dest,
			Via:      route.Via,
			Dev:      route.Dev,
			TableID:  route.TableID,
			Protocol: route.Protocol,
			Metric:   route.Metric,
			Scope:    0,
			Type:     0,
			NextHops: nil,
		}
		if err := f.deleteRoute(ctx, peer, tableID, route.Family, current); err != nil {
			deleteErr = errors.Join(deleteErr, err)
		}
	}
	return deleteErr
}

func (f *FIB) deleteRoute(
	ctx context.Context,
	peer string,
	tableID int,
	family string,
	current netif.CurrentRoute,
) error {
	route := netif.RouteSpec{
		Family:   family,
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
		Family:   prefixFamily(prefix),
		Dest:     prefix.String(),
		Via:      via,
		Dev:      f.cfg.InternalIface,
		TableID:  tableID,
		Metric:   int(f.cfg.Metric),
		Protocol: unix.RTPROT_BGP,
	}
}
