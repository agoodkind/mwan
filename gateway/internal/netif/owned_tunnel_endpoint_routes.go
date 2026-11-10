package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"

	"github.com/vishvananda/netlink"
)

// TunnelEndpointRouteProtocol identifies a tunnel endpoint route in kernel route dumps and route events.
const TunnelEndpointRouteProtocol = 147

const tunnelEndpointRouteFamily = "inet"

type ownedEndpointRoute struct {
	ConnectionID string `json:"connection_id"`
	LinkName     string `json:"link_name"`
	LinkIndex    int    `json:"link_index"`
	LinkIdentity string `json:"link_identity"`
	TableID      int    `json:"table_id"`
	Destination  string `json:"destination"`
	Gateway      string `json:"gateway"`
}

func (record ownedEndpointRoute) spec() RouteSpec {
	return RouteSpec{
		Family: tunnelEndpointRouteFamily, Dest: record.Destination, Via: record.Gateway, Dev: record.LinkName,
		TableID: record.TableID, Metric: 0, Protocol: TunnelEndpointRouteProtocol,
	}
}

func (record ownedEndpointRoute) sameSlot(other ownedEndpointRoute) bool {
	return record.LinkName == other.LinkName && record.LinkIndex == other.LinkIndex &&
		record.LinkIdentity == other.LinkIdentity && record.TableID == other.TableID &&
		record.Destination == other.Destination
}

func (r *OwnedStaticReconciler) saveEndpointRoutes(next []ownedEndpointRoute) error {
	previous := r.journal.EndpointRoutes
	r.journal.EndpointRoutes = next
	if err := r.save(); err != nil {
		r.journal.EndpointRoutes = previous
		return err
	}
	return nil
}

func kernelEndpointRoutes(ctx context.Context, log *slog.Logger, record ownedEndpointRoute) ([]CurrentRoute, error) {
	routes, err := ListTableRoutes(ctx, log, tunnelEndpointRouteFamily, record.TableID)
	if err != nil {
		log.WarnContext(ctx, "tunnel endpoint route read failed", "table_id", record.TableID, "err", err)
		return nil, fmt.Errorf("read tunnel endpoint route table %d: %w", record.TableID, err)
	}
	var matched []CurrentRoute
	for _, route := range routes {
		if route.Dest == record.Destination && route.Metric == 0 {
			matched = append(matched, route)
		}
	}
	return matched, nil
}

// EnsureTunnelEndpointRoute records one IPv4 host route for a tunnel connection in the ownership journal
// before writing the route to the kernel.
func (r *OwnedStaticReconciler) EnsureTunnelEndpointRoute(ctx context.Context, log *slog.Logger, connectionID string, want RouteSpec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if connectionID == "" || want.Family != tunnelEndpointRouteFamily || want.Via == "" || want.Metric != 0 ||
		want.Protocol != TunnelEndpointRouteProtocol {
		return fmt.Errorf("invalid tunnel endpoint route for connection %q: %+v", connectionID, want)
	}
	link, err := linkByName(log, want.Dev)
	if err != nil {
		return err
	}
	record := ownedEndpointRoute{
		ConnectionID: connectionID, LinkName: want.Dev, LinkIndex: link.Attrs().Index,
		LinkIdentity: LinkOwnershipIdentity(link), TableID: want.TableID, Destination: want.Dest, Gateway: want.Via,
	}
	next := slices.Clone(r.journal.EndpointRoutes)
	index := slices.IndexFunc(next, func(old ownedEndpointRoute) bool { return old.ConnectionID == connectionID })
	if index >= 0 && !next[index].sameSlot(record) {
		if err := r.deleteEndpointRoute(ctx, log, next[index]); err != nil {
			return err
		}
	}
	current, err := kernelEndpointRoutes(ctx, log, record)
	if err != nil {
		return err
	}
	for _, route := range current {
		if route.Protocol != TunnelEndpointRouteProtocol || route.Dev != want.Dev {
			return fmt.Errorf("tunnel endpoint route %s in table %d conflicts with an unowned route", want.Dest, want.TableID)
		}
	}
	if index < 0 {
		next = append(next, record)
	} else {
		next[index] = record
	}
	if !slices.Equal(next, r.journal.EndpointRoutes) {
		if err := r.saveEndpointRoutes(next); err != nil {
			return err
		}
	}
	for _, route := range current {
		if route.Via == want.Via {
			return nil
		}
	}
	return replaceTableRouteNetlink(ctx, log, want)
}

func (r *OwnedStaticReconciler) deleteEndpointRoute(ctx context.Context, log *slog.Logger, record ownedEndpointRoute) error {
	link, err := netlink.LinkByIndex(record.LinkIndex)
	if IsLinkNotFound(err) {
		return nil
	}
	if err != nil {
		log.WarnContext(ctx, "tunnel endpoint route link lookup failed", "connection_id", record.ConnectionID, "err", err)
		return fmt.Errorf("find tunnel endpoint route link: %w", err)
	}
	if !LinkMatchesIdentity(link, record.LinkIndex, record.LinkIdentity) {
		return nil
	}
	if link.Attrs().Name != record.LinkName {
		return fmt.Errorf("recorded tunnel endpoint route link %s was renamed to %s", record.LinkName, link.Attrs().Name)
	}
	for _, other := range r.journal.EndpointRoutes {
		if other != record && other.sameSlot(record) {
			return nil
		}
	}
	// The kernel route can still use the previous gateway after a crash following the journal write.
	// Route deletion matches every gateway for the table, destination, link, and protocol in the journal
	// record.
	stale := record.spec()
	stale.Via = ""
	return delTableRouteNetlink(ctx, log, stale)
}

// ReleaseTunnelEndpointRoutes removes every recorded route of a connection absent from keep.
func (r *OwnedStaticReconciler) ReleaseTunnelEndpointRoutes(ctx context.Context, log *slog.Logger, keep map[string]bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var failures error
	for _, record := range slices.Clone(r.journal.EndpointRoutes) {
		if keep[record.ConnectionID] {
			continue
		}
		if err := r.deleteEndpointRoute(ctx, log, record); err != nil {
			failures = errors.Join(failures, fmt.Errorf("delete tunnel endpoint route table=%d dest=%s: %w", record.TableID, record.Destination, err))
			continue
		}
		remaining := slices.DeleteFunc(slices.Clone(r.journal.EndpointRoutes), func(old ownedEndpointRoute) bool { return old == record })
		if err := r.saveEndpointRoutes(remaining); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}
