//go:build linux

package netif

import (
	"context"
	"errors"

	"github.com/vishvananda/netlink"
)

func resolveForwardingRoute(ctx context.Context, packet PacketSelector, uid packetRouteUID) (netlink.Route, *EgressObservationRequiredError, bool, error) {
	var empty netlink.Route
	selected, err := queryPacketRoute(ctx, packet, false, uid)
	if err != nil {
		if forwardingRouteAbsent(err) {
			return empty, nil, false, nil
		}
		return empty, nil, false, err
	}
	if len(selected.route.MultiPath) != 0 {
		return empty, nil, false, &EgressObservationRequiredError{Reason: EgressObservationMultipath, NexthopID: selected.nexthopID}
	}
	if selected.encapsulated || !forwardingRouteSupported(selected.route) || selected.route.LinkIndex <= 0 {
		return empty, nil, false, nil
	}
	fib, err := queryPacketRoute(ctx, packet, true, uid)
	if err != nil {
		var requirement *EgressObservationRequiredError
		if errors.As(err, &requirement) {
			return selected.route, requirement, true, nil
		}
		if forwardingRouteAbsent(err) {
			return empty, nil, false, nil
		}
		return empty, nil, false, err
	}
	if !forwardingFIBMatches(selected, fib) {
		return empty, nil, false, nil
	}
	var requirement *EgressObservationRequiredError
	if len(fib.route.MultiPath) > 1 {
		requirement = &EgressObservationRequiredError{Reason: EgressObservationMultipath, NexthopID: fib.nexthopID}
	}
	if fib.nexthopID == 0 {
		return selected.route, requirement, true, nil
	}
	group, supported, err := inspectForwardingNexthop(ctx, fib.nexthopID, selected.route)
	if err != nil || !supported {
		return empty, nil, false, err
	}
	if group {
		requirement = &EgressObservationRequiredError{Reason: EgressObservationNexthopGroup, NexthopID: fib.nexthopID}
	}
	return selected.route, requirement, true, nil
}

func forwardingFIBMatches(selected, fib packetRouteObservation) bool {
	if fib.encapsulated || !forwardingRouteSupported(fib.route) || fib.route.Table != selected.route.Table || fib.route.Family != selected.route.Family ||
		(fib.route.LinkIndex > 0 && fib.route.LinkIndex != selected.route.LinkIndex) ||
		(fib.route.Gw != nil && !fib.route.Gw.Equal(selected.route.Gw)) ||
		(selected.nexthopID != 0 && fib.nexthopID != 0 && selected.nexthopID != fib.nexthopID) {
		return false
	}
	if len(fib.route.MultiPath) == 0 {
		return true
	}
	if len(fib.hopEncapsulation) != len(fib.route.MultiPath) {
		return false
	}
	for index, hop := range fib.route.MultiPath {
		if !fib.hopEncapsulation[index] && hop != nil && hop.LinkIndex == selected.route.LinkIndex && hop.Gw.Equal(selected.route.Gw) &&
			hop.Encap == nil && hop.Via == nil && hop.NewDst == nil {
			return true
		}
	}
	return false
}
