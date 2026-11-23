package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Linux UAPI defines IP6_RT_PRIO_USER as the metric for an omitted IPv6 metric.
const ipv6UserRouteMetric = 1024

const (
	ipv4PrefixLength = 32
	ipv6PrefixLength = 128
)

var errInvalidGatewayAddress = errors.New("not a valid IP")

// Linux can retain expired routes in dumps after rejecting them for forwarding.
func listUsableRoutes(family int, filter *netlink.Route, mask uint64) ([]netlink.Route, error) {
	routes, err := netlink.RouteListFiltered(family, filter, mask)
	if err != nil {
		slog.Warn("netif: usable route dump failed", "family", family, "err", err)

		return nil, fmt.Errorf("list usable routes for family %d: %w", family, err)
	}

	current := routes[:0]
	for _, route := range routes {
		if route.CacheInfo != nil && route.CacheInfo.Expires < 0 {
			continue
		}

		current = append(current, route)
	}

	return current, nil
}

// getTableDefaultNetlink finds the default route in the named table for the
// given family. Returns (nil, nil) when no default route exists. Returns
// (nil, nil) for ENOENT-equivalent errors so callers can treat "table empty"
// as "no default present" (matches the prior shellout behavior).
func getTableDefaultNetlink(
	log *slog.Logger,
	family string,
	tableID int,
) (*CurrentRoute, error) {
	famConst := familyToNetlink(family)
	filter := &netlink.Route{Table: tableID}

	start := realClock{}.Now()
	routes, err := listUsableRoutes(famConst, filter, netlink.RT_FILTER_TABLE)
	dur := realClock{}.Now().Sub(start)
	log.Debug(
		"route: RouteListFiltered (default lookup)",
		"family",
		family,
		"table_id",
		tableID,
		"count",
		len(routes),
		"duration_ms",
		dur.Milliseconds(),
		"err",
		err,
	)
	if err != nil {
		// Treat "table doesn't exist yet" as no-default.
		if errors.Is(err, syscall.ENOENT) {
			return nil, nil
		}

		log.Warn(
			"route: RouteListFiltered failed",
			"family",
			family,
			"table_id",
			tableID,
			"err",
			err,
		)

		return nil, fmt.Errorf("RouteListFiltered(default,%s,%d): %w", family, tableID, err)
	}

	for _, route := range routes {
		if !isDefaultRoute(route, famConst) {
			continue
		}

		cur, err := routeToCurrent(log, route)
		if err != nil {
			return nil, fmt.Errorf("routeToCurrent(default,%s,%d): %w", family, tableID, err)
		}

		return cur, nil
	}

	return nil, nil
}

// isDefaultRoute reports whether route is the family's default route. For IPv4,
// netlink represents default as Dst==nil with prefix length zero. Same for
// v6. Some kernels report Dst as a 0.0.0.0/0 or ::/0 explicitly; both forms
// are accepted here.
func isDefaultRoute(route netlink.Route, family int) bool {
	if route.Dst == nil {
		return true
	}

	ones, _ := route.Dst.Mask.Size()
	if ones != 0 {
		return false
	}

	switch family {
	case unix.AF_INET:
		return route.Dst.IP.To4() != nil && route.Dst.IP.To4().Equal(net.IPv4zero)
	case unix.AF_INET6:
		return route.Dst.IP.To4() == nil && route.Dst.IP.Equal(net.IPv6unspecified)
	}

	return false
}

// routeToCurrent retains every next hop and stores the first hop in Via and Dev.
func routeToCurrent(log *slog.Logger, route netlink.Route) (*CurrentRoute, error) {
	dest := "default"
	if route.Dst != nil {
		prefixLength, _ := route.Dst.Mask.Size()
		if prefixLength != 0 {
			dest = route.Dst.String()
		}
	}

	cur := new(CurrentRoute)
	cur.Dest = dest
	cur.TableID = route.Table
	cur.Protocol = int(route.Protocol)
	cur.Metric = route.Priority
	cur.Scope = int(route.Scope)
	cur.Type = route.Type
	switch {
	case route.Family == unix.AF_INET:
		cur.Family = tunnelEndpointRouteFamily
	case route.Family == unix.AF_INET6:
		cur.Family = string(httpFamilyV6)
	case route.Dst != nil && route.Dst.IP.To4() != nil:
		cur.Family = tunnelEndpointRouteFamily
	case route.Dst != nil || route.Gw != nil:
		cur.Family = string(httpFamilyV6)
	}

	if route.Gw != nil {
		cur.Via = route.Gw.String()
	}

	if route.LinkIndex != 0 {
		var hop RouteNextHop
		hop.Via = cur.Via
		hop.LinkIndex = route.LinkIndex
		hop.Weight = 1
		cur.NextHops = append(cur.NextHops, hop)
	}

	for _, hop := range route.MultiPath {
		if hop == nil {
			continue
		}

		var observed RouteNextHop
		observed.LinkIndex = hop.LinkIndex
		observed.Weight = hop.Hops + 1
		if hop.Gw != nil {
			observed.Via = hop.Gw.String()
		}

		cur.NextHops = append(cur.NextHops, observed)
	}

	for hopIndex := range cur.NextHops {
		link, err := netlink.LinkByIndex(cur.NextHops[hopIndex].LinkIndex)
		if err != nil {
			log.Warn(
				"route: LinkByIndex failed",
				"index",
				cur.NextHops[hopIndex].LinkIndex,
				"err",
				err,
			)

			return nil, fmt.Errorf("LinkByIndex(%d): %w", cur.NextHops[hopIndex].LinkIndex, err)
		}

		cur.NextHops[hopIndex].Dev = link.Attrs().Name
	}

	if len(cur.NextHops) > 0 {
		cur.Dev = cur.NextHops[0].Dev
		cur.Via = cur.NextHops[0].Via
	}

	return cur, nil
}

// replaceTableDefaultNetlink installs (or replaces) a default route in the
// named table. Uses RouteReplace which is "add or update" in one syscall.
func replaceTableDefaultNetlink(
	ctx context.Context,
	log *slog.Logger,
	want RouteSpec,
) error {
	_ = ctx
	link, err := netlink.LinkByName(want.Dev)
	if err != nil {
		return fmt.Errorf("link %q: %w", want.Dev, err)
	}

	famConst := familyToNetlink(want.Family)

	gatewayIP := net.ParseIP(want.Via)
	if gatewayIP == nil {
		return fmt.Errorf("parse gateway %q: %w", want.Via, errInvalidGatewayAddress)
	}

	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Table:     want.TableID,
		Gw:        gatewayIP,
		Family:    famConst,
		Priority:  want.Metric,
		Protocol:  netlink.RouteProtocol(want.Protocol),
	}
	// Dst nil means "default" for the family.

	start := realClock{}.Now()
	err = netlink.RouteReplace(route)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteReplace (default)",
		"family",
		want.Family,
		"table_id",
		want.TableID,
		"via",
		want.Via,
		"dev",
		want.Dev,
		"duration_ms",
		dur.Milliseconds(),
		"err",
		err,
	)
	if err != nil {
		log.WarnContext(
			ctx,
			"route: RouteReplace (default) failed",
			"family",
			want.Family,
			"table_id",
			want.TableID,
			"err",
			err,
		)

		return fmt.Errorf("RouteReplace(default,%s,%d): %w", want.Family, want.TableID, err)
	}

	return nil
}

func replaceTableRouteNetlink(
	ctx context.Context,
	log *slog.Logger,
	want RouteSpec,
) error {
	link, err := netlink.LinkByName(want.Dev)
	if err != nil {
		return fmt.Errorf("link %q: %w", want.Dev, err)
	}

	route, err := buildTableRoute(log, want, link)
	if err != nil {
		return err
	}

	desired := normalizedTableRouteRequest(*route)
	current, err := listUsableRoutes(
		desired.Family,
		&desired,
		netlink.RT_FILTER_TABLE|netlink.RT_FILTER_DST,
	)
	if err != nil && !errors.Is(err, syscall.ENOENT) {
		return err
	}

	for _, existing := range current {
		if reflect.DeepEqual(normalizedTableRoute(existing), desired) {
			log.DebugContext(
				ctx,
				"route: prefix already correct",
				"dest",
				want.Dest,
				"dev",
				want.Dev,
			)

			return nil
		}
	}

	start := realClock{}.Now()
	err = netlink.RouteReplace(route)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteReplace (prefix)",
		"family",
		want.Family,
		"table_id",
		want.TableID,
		"dest",
		want.Dest,
		"via",
		want.Via,
		"dev",
		want.Dev,
		"duration_ms",
		dur.Milliseconds(),
		"err",
		err,
	)
	if err != nil {
		log.WarnContext(
			ctx,
			"route: RouteReplace (prefix) failed",
			"family",
			want.Family,
			"table_id",
			want.TableID,
			"dest",
			want.Dest,
			"err",
			err,
		)

		return fmt.Errorf(
			"RouteReplace(prefix,%s,%d,%s): %w",
			want.Family,
			want.TableID,
			want.Dest,
			err,
		)
	}

	return nil
}

func normalizedTableRouteRequest(route netlink.Route) netlink.Route {
	if route.Type == 0 {
		route.Type = unix.RTN_UNICAST
	}

	if route.Protocol == 0 {
		route.Protocol = unix.RTPROT_BOOT
	}

	if route.Table == 0 {
		route.Table = unix.RT_TABLE_MAIN
	}

	if route.Family == unix.AF_INET6 {
		// IPv6 route dumps report universe scope even for an on-link request.
		route.Scope = netlink.SCOPE_UNIVERSE
		if route.Priority == 0 {
			route.Priority = ipv6UserRouteMetric
		}
	}

	return normalizedTableRoute(route)
}

func normalizedTableRoute(route netlink.Route) netlink.Route {
	if route.Dst != nil {
		route.Dst = &net.IPNet{IP: route.Dst.IP.To16(), Mask: route.Dst.Mask}
	}

	route.Src = route.Src.To16()
	route.Gw = route.Gw.To16()
	// Cache usage and age do not change a permanent route's forwarding policy.
	if route.CacheInfo != nil && route.CacheInfo.Expires == 0 {
		route.CacheInfo = nil
	}

	return route
}

func buildTableRoute(log *slog.Logger, want RouteSpec, link netlink.Link) (*netlink.Route, error) {
	_, dst, err := net.ParseCIDR(want.Dest)
	if err != nil {
		log.Warn("route: ParseCIDR failed", "dest", want.Dest, "err", err)
		return nil, fmt.Errorf("ParseCIDR(%q): %w", want.Dest, err)
	}

	route := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Table:     want.TableID,
		Dst:       dst,
		Family:    familyToNetlink(want.Family),
		Priority:  want.Metric,
		Protocol:  netlink.RouteProtocol(want.Protocol),
	}
	if want.Via == "" {
		route.Scope = netlink.SCOPE_LINK
		return route, nil
	}

	gateway := net.ParseIP(want.Via)
	if gateway == nil {
		log.Warn("route: parse gateway failed", "gateway", want.Via)

		return nil, fmt.Errorf("parse gateway %q: %w", want.Via, errInvalidGatewayAddress)
	}

	route.Gw = gateway

	return route, nil
}

// delTableDefaultNetlink removes the default route from the table. ENOENT
// (no such route) is swallowed and logged at DEBUG, matching the behaviour
// of the prior `ip route del` shellout which exited nonzero but the daemon
// treated as success when called via the wrapper.
func delTableDefaultNetlink(
	ctx context.Context,
	log *slog.Logger,
	family string,
	tableID int,
) error {
	_ = ctx
	famConst := familyToNetlink(family)

	destination := &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, ipv4PrefixLength)}
	if famConst == unix.AF_INET6 {
		destination = &net.IPNet{IP: net.IPv6zero, Mask: net.CIDRMask(0, ipv6PrefixLength)}
	}

	r := &netlink.Route{Table: tableID, Family: famConst, Dst: destination}
	start := realClock{}.Now()
	err := netlink.RouteDel(r)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteDel (default)",
		"family",
		family,
		"table_id",
		tableID,
		"duration_ms",
		dur.Milliseconds(),
		"err",
		err,
	)
	if err != nil && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH)) {
		log.DebugContext(ctx, "route: nothing to delete (already absent)")
		return nil
	}

	if err != nil {
		log.WarnContext(
			ctx,
			"route: RouteDel (default) failed",
			"family",
			family,
			"table_id",
			tableID,
			"err",
			err,
		)

		return fmt.Errorf("RouteDel(default,%s,%d): %w", family, tableID, err)
	}

	return nil
}

func delTableRouteNetlink(ctx context.Context, log *slog.Logger, want RouteSpec) error {
	_, destination, err := net.ParseCIDR(want.Dest)
	if err != nil {
		log.WarnContext(ctx, "route: ParseCIDR failed", "dest", want.Dest, "err", err)
		return fmt.Errorf("ParseCIDR(%q): %w", want.Dest, err)
	}

	// The kernel matches only the fields a delete request sets. A request
	// without a device, metric, or gateway removes the lowest-metric route for
	// the destination on any device.
	route := &netlink.Route{
		Table:    want.TableID,
		Dst:      destination,
		Family:   familyToNetlink(want.Family),
		Protocol: netlink.RouteProtocol(want.Protocol),
		Priority: want.Metric,
	}
	if want.Dev != "" {
		link, linkErr := netlink.LinkByName(want.Dev)
		if IsLinkNotFound(linkErr) {
			// The kernel removes every route that uses a device when the kernel
			// removes that device.
			log.DebugContext(ctx, "route: nothing to delete (device absent)", "dev", want.Dev)
			return nil
		}

		if linkErr != nil {
			return fmt.Errorf("link %q: %w", want.Dev, linkErr)
		}

		route.LinkIndex = link.Attrs().Index
	}

	if want.Via != "" {
		gateway := net.ParseIP(want.Via)
		if gateway == nil {
			log.WarnContext(ctx, "route: parse gateway failed", "gateway", want.Via)

			return fmt.Errorf("parse gateway %q: %w", want.Via, errInvalidGatewayAddress)
		}

		route.Gw = gateway
	}

	start := realClock{}.Now()
	err = netlink.RouteDel(route)
	duration := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteDel (prefix)",
		"family",
		want.Family,
		"table_id",
		want.TableID,
		"dest",
		want.Dest,
		"via",
		want.Via,
		"dev",
		want.Dev,
		"metric",
		want.Metric,
		"duration_ms",
		duration.Milliseconds(),
		"err",
		err,
	)
	if err != nil && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH)) {
		log.DebugContext(ctx, "route: nothing to delete (already absent)")
		return nil
	}

	if err != nil {
		log.WarnContext(
			ctx,
			"route: RouteDel (prefix) failed",
			"family",
			want.Family,
			"table_id",
			want.TableID,
			"dest",
			want.Dest,
			"err",
			err,
		)

		return fmt.Errorf(
			"RouteDel(prefix,%s,%d,%s): %w",
			want.Family,
			want.TableID,
			want.Dest,
			err,
		)
	}

	return nil
}

// IfaceDefaultGateway returns the main-table default-route gateway for iface.
// It returns an empty string when iface has no gateway.
//
// Example:
//
//	IfaceDefaultGateway("inet", "wan0")
func IfaceDefaultGateway(family string, iface string) (string, error) {
	log := slog.Default().With(
		"component",
		"route",
		"iface",
		iface,
		"family",
		family,
		"op",
		"iface-default-gateway",
	)
	link, err := netlink.LinkByName(iface)
	if err != nil {
		return "", fmt.Errorf("link %q: %w", iface, err)
	}

	famConst := familyToNetlink(family)
	filter := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Table:     unix.RT_TABLE_MAIN,
	}
	mask := netlink.RT_FILTER_OIF | netlink.RT_FILTER_TABLE

	start := realClock{}.Now()
	routes, err := listUsableRoutes(famConst, filter, mask)
	dur := realClock{}.Now().Sub(start)
	log.Debug(
		"route: RouteListFiltered (iface default gateway)",
		"count",
		len(routes),
		"duration_ms",
		dur.Milliseconds(),
		"err",
		err,
	)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return "", nil
		}

		log.Warn(
			"route: RouteListFiltered (iface default gateway) failed",
			"iface",
			iface,
			"family",
			family,
			"err",
			err,
		)

		return "", fmt.Errorf("RouteListFiltered(iface-default,%s,%s): %w", iface, family, err)
	}

	for _, route := range routes {
		if !isDefaultRoute(route, famConst) {
			continue
		}

		current, err := routeToCurrent(log, route)
		if err != nil {
			return "", fmt.Errorf("routeToCurrent(iface-default,%s,%s): %w", family, iface, err)
		}

		if current.Via != "" {
			return current.Via, nil
		}
	}

	return "", nil
}
