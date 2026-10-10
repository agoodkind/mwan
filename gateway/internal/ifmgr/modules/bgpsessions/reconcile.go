package bgpsessions

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

const (
	familyV4           = "inet"
	familyV6           = "inet6"
	listedDefaultRoute = "default"
	ipv6DefaultPrefix  = "::/0"
)

// Reconcile starts or stops each session for its interface and tunnel endpoint route, then deletes stale session routes.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()
	if m.stopped {
		return nil
	}
	var reconcileErr error
	for _, entry := range m.sessions {
		if err := m.reconcileSession(ctx, log, entry); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
	}
	if err := m.sweepStaleRoutes(ctx, log); err != nil {
		reconcileErr = errors.Join(reconcileErr, err)
	}
	for _, entry := range m.sessions {
		m.publish(entry.cfg.Key())
	}
	return reconcileErr
}

func (m *Module) reconcileSession(ctx context.Context, log *slog.Logger, entry *managedSession) error {
	name := entry.cfg.Config.Name
	transport, err := sessionTransport(ctx, log, entry.cfg)
	if err != nil {
		return err
	}
	if entry.running && transport != entry.transport {
		log.InfoContext(ctx, "bgp_sessions: session transport changed",
			"session", name, "previous", entry.transport, "current", transport)
		m.stopLocked(ctx, log, entry)
	}
	if transport == "" || entry.running {
		return nil
	}
	if err := m.recordOwner(ctx, log, sessionOwner(entry.cfg)); err != nil {
		return err
	}
	if err := entry.session.Start(ctx); err != nil {
		log.WarnContext(ctx, "bgp_sessions: session start failed", "session", name, "err", err)
		return fmt.Errorf("start bgp session %s: %w", name, err)
	}
	entry.running, entry.transport = true, transport
	return nil
}

func (m *Module) stopLocked(ctx context.Context, log *slog.Logger, entry *managedSession) {
	if !entry.running {
		return
	}
	if err := entry.session.Stop(ctx); err != nil {
		log.WarnContext(ctx, "bgp_sessions: session stop failed", "session", entry.cfg.Config.Name, "err", err)
	}
	entry.running, entry.transport = false, ""
}

func sessionTransport(ctx context.Context, log *slog.Logger, cfg Session) (string, error) {
	link, err := net.InterfaceByName(cfg.Iface)
	if err != nil {
		log.DebugContext(ctx, "bgp_sessions: session interface absent", "iface", cfg.Iface, "err", err)
		return "", nil
	}
	if cfg.EndpointDest == "" {
		return fmt.Sprintf("index=%d", link.Index), nil
	}
	routes, err := netif.ListProtocolRoutes(ctx, log, familyV4, cfg.EndpointTable, netif.TunnelEndpointRouteProtocol)
	if err != nil {
		log.WarnContext(ctx, "bgp_sessions: endpoint route read failed", "table_id", cfg.EndpointTable, "err", err)
		return "", fmt.Errorf("read tunnel endpoint routes of table %d: %w", cfg.EndpointTable, err)
	}
	for _, route := range routes {
		if route.Dest == cfg.EndpointDest {
			return fmt.Sprintf("index=%d endpoint=%s via %s", link.Index, route.Dev, route.Via), nil
		}
	}
	return "", nil
}

// The address module installs configured routes with the protocol number of
// the session routes. The sweep must read only the interfaces and metrics in
// the journal.
func (m *Module) sweepStaleRoutes(ctx context.Context, log *slog.Logger) error {
	if len(m.owners) == 0 {
		return nil
	}
	// A session can install a route after the listing. The accepted prefixes
	// that the module reads after the listing include every listed live route.
	routes, err := netif.ListProtocolRoutes(ctx, log, familyV6, unix.RT_TABLE_MAIN, unix.RTPROT_BGP)
	if err != nil {
		log.WarnContext(ctx, "bgp_sessions: BGP route read failed", "err", err)
		return fmt.Errorf("read main-table BGP routes: %w", err)
	}
	var sweepErr error
	var staleDestinations []string
	for _, route := range routes {
		if !slices.Contains(m.owners, routeOwner{Interface: route.Dev, Metric: route.Metric}) {
			continue
		}
		if route.Dest == listedDefaultRoute {
			route.Dest = ipv6DefaultPrefix
		}
		if m.accepts(route) || m.configured(route) {
			continue
		}
		stale := netif.RouteSpec{
			Family: familyV6, Dest: route.Dest, Via: route.Via, Dev: route.Dev,
			TableID: unix.RT_TABLE_MAIN, Metric: route.Metric, Protocol: unix.RTPROT_BGP,
		}
		staleDestinations = append(staleDestinations, route.Dest+" dev "+route.Dev)
		if err := netif.DeleteTableRoute(ctx, log, stale); err != nil {
			log.WarnContext(ctx, "bgp_sessions: stale BGP route removal failed", "dest", route.Dest, "err", err)
			sweepErr = errors.Join(sweepErr, fmt.Errorf("remove stale BGP route %s on %s: %w", route.Dest, route.Dev, err))
		}
	}
	if len(staleDestinations) > 0 {
		log.InfoContext(ctx, "bgp_sessions: stale BGP routes found", "routes", staleDestinations)
	}
	if sweepErr != nil {
		return sweepErr
	}
	return m.releaseRemovedOwners(ctx, log)
}

func (m *Module) configured(route netif.CurrentRoute) bool {
	return slices.ContainsFunc(m.cfg.ConfiguredRoutes, func(configured ConfiguredRoute) bool {
		return configured.Interface == route.Dev && configured.Metric == route.Metric &&
			configured.Dest == route.Dest && (configured.Via == "" || configured.Via == route.Via)
	})
}

func (m *Module) accepts(route netif.CurrentRoute) bool {
	prefix, err := netip.ParsePrefix(route.Dest)
	if err != nil {
		return false
	}
	for _, entry := range m.sessions {
		if !entry.running || entry.cfg.Iface != route.Dev || int(entry.cfg.Config.RouteMetric) != route.Metric {
			continue
		}
		if slices.Contains(entry.session.State().Accepted, prefix.Masked()) {
			return true
		}
	}
	return false
}
