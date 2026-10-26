//go:build linux

package netif

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/agoodkind/netlink"
)

func resolveForwardingRoute(ctx context.Context, log *slog.Logger, handle *netlink.Handle, packet PacketSelector) (netlink.Route, *EgressObservationRequiredError, bool, error) {
	var empty netlink.Route
	options := packet.routeOptions()
	routes, err := handle.RouteGetWithOptions(packet.Destination.Addr().AsSlice(), options)
	if err != nil {
		if forwardingRouteAbsent(err) {
			return empty, nil, false, nil
		}
		log.WarnContext(ctx, "forwarded packet route query failed", "packet", packet, "err", err)
		return empty, nil, false, fmt.Errorf("resolve forwarded packet route: %w", err)
	}
	if len(routes) == 0 {
		return empty, nil, false, nil
	}
	if len(routes) != 1 {
		return empty, nil, false, &EgressObservationRequiredError{Reason: EgressObservationMultipleRoutes, NexthopID: 0}
	}
	selected := routes[0]
	if len(selected.MultiPath) != 0 {
		return empty, nil, false, &EgressObservationRequiredError{Reason: EgressObservationMultipath, NexthopID: selected.NHID}
	}
	if !forwardingRouteSupported(selected) || selected.LinkIndex <= 0 {
		return empty, nil, false, nil
	}
	if err := ctx.Err(); err != nil {
		return empty, nil, false, fmt.Errorf("inspect forwarding route policy: %w", err)
	}
	options.FIBMatch = true
	matched, err := handle.RouteGetWithOptions(packet.Destination.Addr().AsSlice(), options)
	if err != nil {
		if forwardingRouteAbsent(err) {
			return empty, nil, false, nil
		}
		log.WarnContext(ctx, "matched forwarding route query failed", "packet", packet, "err", err)
		return empty, nil, false, fmt.Errorf("inspect matched forwarding route: %w", err)
	}
	if len(matched) == 0 {
		return empty, nil, false, nil
	}
	if len(matched) != 1 {
		return selected, &EgressObservationRequiredError{Reason: EgressObservationMultipleRoutes, NexthopID: 0}, true, nil
	}
	fib := matched[0]
	if !forwardingFIBMatches(selected, fib) {
		return empty, nil, false, nil
	}
	var requirement *EgressObservationRequiredError
	if len(fib.MultiPath) > 1 {
		requirement = &EgressObservationRequiredError{Reason: EgressObservationMultipath, NexthopID: fib.NHID}
	}
	if fib.NHID == 0 {
		return selected, requirement, true, nil
	}
	group, supported, err := inspectForwardingNexthop(ctx, log, handle, fib.NHID, selected)
	if err != nil || !supported {
		return empty, nil, false, err
	}
	if group {
		requirement = &EgressObservationRequiredError{Reason: EgressObservationNexthopGroup, NexthopID: fib.NHID}
	}
	return selected, requirement, true, nil
}

func forwardingFIBMatches(selected, fib netlink.Route) bool {
	if !forwardingRouteSupported(fib) || fib.Table != selected.Table || fib.Family != selected.Family ||
		(fib.LinkIndex > 0 && fib.LinkIndex != selected.LinkIndex) ||
		(fib.Gw != nil && !fib.Gw.Equal(selected.Gw)) {
		return false
	}
	if len(fib.MultiPath) == 0 {
		return true
	}
	for _, hop := range fib.MultiPath {
		if hop != nil && hop.LinkIndex == selected.LinkIndex && hop.Gw.Equal(selected.Gw) &&
			hop.Encap == nil && hop.Via == nil && hop.NewDst == nil {
			return true
		}
	}
	return false
}
