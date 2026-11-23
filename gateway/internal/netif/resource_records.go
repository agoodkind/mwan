package netif

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const ipv4UnspecifiedGateway = "0.0.0.0"

var (
	errRecordedLinkNameChanged = errors.New("recorded link name changed")
	errUnownedRouteConflict    = errors.New("conflicts with an unowned route")
)

func sameStaticRoute(route netlink.Route, value ownedStaticObject) bool {
	return routeMatchesDestination(route, value.Destination) && route.Table == unix.RT_TABLE_MAIN &&
		route.Priority == value.Metric &&
		route.Protocol == OwnedStaticRouteProtocol &&
		route.LinkIndex == value.LinkIndex &&
		route.Gw.Equal(ownedRouteGateway(value.Gateway))
}

func ownedRouteGateway(gateway string) net.IP {
	if gateway == ipv4UnspecifiedGateway || gateway == "::" {
		return nil
	}

	return net.ParseIP(gateway)
}

func routeMatchesDestination(route netlink.Route, destination string) bool {
	if destination == "" {
		if route.Dst == nil {
			return true
		}

		ones, _ := route.Dst.Mask.Size()

		return ones == 0
	}

	return route.Dst != nil && route.Dst.String() == destination
}

func (r *OwnedStaticReconciler) recordedRoute(route netlink.Route, value ownedStaticObject) bool {
	for _, old := range r.journal.Objects {
		if old.ConnectionID != value.ConnectionID || old.Family != value.Family ||
			old.Gateway == "" ||
			old.LinkIndex != value.LinkIndex ||
			old.LinkIdentity != value.LinkIdentity {
			continue
		}

		if sameStaticRoute(route, old) {
			return true
		}
	}

	return false
}

func (r *OwnedStaticReconciler) ensureRoute(value ownedStaticObject) error {
	routes, err := netlink.RouteListFiltered(
		staticFamily(value.Family),
		&netlink.Route{Table: unix.RT_TABLE_MAIN},
		netlink.RT_FILTER_TABLE,
	)
	if err != nil {
		return fmt.Errorf("list main routes: %w", err)
	}

	found := false
	replace := false

	for _, route := range routes {
		if !routeMatchesDestination(route, value.Destination) || route.Priority != value.Metric {
			continue
		}

		if !r.recordedRoute(route, value) {
			if value.Destination == "" {
				return fmt.Errorf(
					"default route metric %d %w",
					value.Metric,
					errUnownedRouteConflict,
				)
			}

			return fmt.Errorf(
				"route %s metric %d %w",
				value.Destination,
				value.Metric,
				errUnownedRouteConflict,
			)
		}

		if sameStaticRoute(route, value) {
			found = true
		} else {
			replace = true
		}
	}

	if found {
		return nil
	}

	if !r.recorded(value) {
		if err := r.reserve(value); err != nil {
			return err
		}
	}

	route := &netlink.Route{
		LinkIndex: value.LinkIndex,
		Table:     unix.RT_TABLE_MAIN,
		Family:    staticFamily(value.Family),
		Gw:        ownedRouteGateway(value.Gateway),
		Priority:  value.Metric,
		Protocol:  OwnedStaticRouteProtocol,
	}
	if value.Gateway == ipv4UnspecifiedGateway {
		route.Scope = netlink.SCOPE_LINK
	}

	if value.Destination != "" {
		_, destination, parseErr := net.ParseCIDR(value.Destination)
		if parseErr != nil {
			return fmt.Errorf("parse owned route destination %s: %w", value.Destination, parseErr)
		}

		route.Dst = destination
	}

	var writeErr error
	if replace {
		writeErr = netlink.RouteReplace(route)
	} else {
		writeErr = netlink.RouteAdd(route)
	}

	if writeErr != nil {
		slog.Warn(
			"owned route write failed",
			"destination",
			value.Destination,
			"gateway",
			value.Gateway,
			"metric",
			value.Metric,
			"err",
			writeErr,
		)

		return fmt.Errorf("write owned route: %w", writeErr)
	}

	return nil
}

func (r *OwnedStaticReconciler) pruneFamily(
	base ownedStaticObject,
	desired map[string]bool,
	retention RecordedRetention,
) error {
	old := append([]ownedStaticObject(nil), r.journal.Objects...)
	for _, value := range old {
		if value.Scope != base.Scope || value.ConnectionID != base.ConnectionID ||
			value.Family != base.Family {
			continue
		}

		matchesLink := value.LinkIndex == base.LinkIndex &&
			value.LinkIdentity == base.LinkIdentity &&
			value.LinkName == base.LinkName
		if matchesLink &&
			(desired[staticObjectKey(
				value,
			)] || retention.All || retainedPrefix(value, retention.Prefixes)) {
			continue
		}

		if err := r.remove(value); err != nil {
			return err
		}
	}

	return nil
}

func retainedPrefix(value ownedStaticObject, prefixes map[netip.Prefix]bool) bool {
	if value.Prefix == "" || len(prefixes) == 0 {
		return false
	}

	prefix, err := netip.ParsePrefix(value.Prefix)

	return err == nil && prefixes[prefix]
}

func (r *OwnedStaticReconciler) remove(value ownedStaticObject) error {
	link, err := netlink.LinkByIndex(value.LinkIndex)
	if IsLinkNotFound(err) {
		return r.forget(value)
	}

	if err != nil {
		slog.Warn("recorded link lookup failed", "connection_id", value.ConnectionID, "err", err)

		return fmt.Errorf("find recorded link: %w", err)
	}

	if !LinkMatchesIdentity(link, value.LinkIndex, value.LinkIdentity) {
		return r.forget(value)
	}

	if link.Attrs().Name != value.LinkName {
		return fmt.Errorf("%w for %s", errRecordedLinkNameChanged, value.ConnectionID)
	}

	if value.Prefix != "" {
		if err := removeStaticAddress(link, value); err != nil {
			return err
		}
	} else if err := removeStaticRoute(value); err != nil {
		return err
	}

	return r.forget(value)
}

func removeStaticAddress(link netlink.Link, value ownedStaticObject) error {
	prefix, err := netip.ParsePrefix(value.Prefix)
	if err != nil {
		slog.Warn("recorded prefix parse failed", "prefix", value.Prefix, "err", err)

		return fmt.Errorf("parse recorded prefix: %w", err)
	}

	addresses, err := netlink.AddrList(link, staticFamily(value.Family))
	if err != nil {
		slog.Warn(
			"recorded address listing failed",
			"connection_id",
			value.ConnectionID,
			"err",
			err,
		)

		return fmt.Errorf("list recorded addresses: %w", err)
	}

	for _, address := range addresses {
		if !addressMatches(address, prefix) {
			continue
		}

		if err := netlink.AddrDel(link, &address); err != nil {
			slog.Warn("recorded address delete failed", "prefix", value.Prefix, "err", err)

			return fmt.Errorf("delete recorded address: %w", err)
		}
	}

	return nil
}

func removeStaticRoute(value ownedStaticObject) error {
	routes, err := netlink.RouteListFiltered(
		staticFamily(value.Family),
		&netlink.Route{Table: unix.RT_TABLE_MAIN},
		netlink.RT_FILTER_TABLE,
	)
	if err != nil {
		slog.Warn("recorded route listing failed", "connection_id", value.ConnectionID, "err", err)

		return fmt.Errorf("list recorded routes: %w", err)
	}

	for _, route := range routes {
		if !sameStaticRoute(route, value) {
			continue
		}

		if err := netlink.RouteDel(&route); err != nil {
			slog.Warn(
				"recorded route delete failed",
				"gateway",
				value.Gateway,
				"metric",
				value.Metric,
				"err",
				err,
			)

			return fmt.Errorf("delete recorded route: %w", err)
		}
	}

	return nil
}
