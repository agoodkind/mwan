package netif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"syscall"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// getTableDefaultNetlink finds the default route in the named table for the
// given family. Returns (nil, nil) when no default route exists. Returns
// (nil, nil) for ENOENT-equivalent errors so callers can treat "table empty"
// as "no default present" (matches the prior shellout behavior).
func getTableDefaultNetlink(
	log *slog.Logger, family string, tableID int,
) (*CurrentRoute, error) {
	famConst := familyToNetlink(family)
	filter := &netlink.Route{Table: tableID}

	start := realClock{}.Now()
	routes, err := netlink.RouteListFiltered(famConst, filter, netlink.RT_FILTER_TABLE)
	dur := realClock{}.Now().Sub(start)
	log.Debug(
		"route: RouteListFiltered (default lookup)",
		"family", family, "table_id", tableID,
		"count", len(routes),
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		// Treat "table doesn't exist yet" as no-default.
		if errors.Is(err, syscall.ENOENT) {
			return nil, nil
		}
		log.Warn("route: RouteListFiltered failed",
			"family", family, "table_id", tableID, "err", err)
		return nil, fmt.Errorf("RouteListFiltered(default,%s,%d): %w", family, tableID, err)
	}

	for _, r := range routes {
		if !isDefaultRoute(r, famConst) {
			continue
		}
		cur, err := routeToCurrent(log, r)
		if err != nil {
			return nil, fmt.Errorf("routeToCurrent(default,%s,%d): %w", family, tableID, err)
		}
		return cur, nil
	}
	return nil, nil
}

// isDefaultRoute reports whether r is the family's default route. For IPv4
// netlink represents default as Dst==nil with IPv4 prefix length 0. Same for
// v6. Some kernels report Dst as a 0.0.0.0/0 or ::/0 explicitly; both forms
// are accepted here.
func isDefaultRoute(r netlink.Route, family int) bool {
	if r.Dst == nil {
		return true
	}
	ones, _ := r.Dst.Mask.Size()
	if ones != 0 {
		return false
	}
	switch family {
	case unix.AF_INET:
		return r.Dst.IP.To4() != nil && r.Dst.IP.To4().Equal(net.IPv4zero)
	case unix.AF_INET6:
		return r.Dst.IP.To4() == nil && r.Dst.IP.Equal(net.IPv6unspecified)
	}
	return false
}

// routeToCurrent converts a netlink.Route to our CurrentRoute. Multipath routes
// use the first next hop because CurrentRoute models one gateway and interface.
func routeToCurrent(log *slog.Logger, r netlink.Route) (*CurrentRoute, error) {
	dest := "default"
	if r.Dst != nil {
		prefixLength, _ := r.Dst.Mask.Size()
		if prefixLength != 0 {
			dest = r.Dst.String()
		}
	}
	cur := &CurrentRoute{
		Dest:   dest,
		Via:    "",
		Dev:    "",
		Metric: r.Priority,
	}
	if r.Gw != nil {
		cur.Via = r.Gw.String()
	} else if len(r.MultiPath) > 0 && r.MultiPath[0].Gw != nil {
		cur.Via = r.MultiPath[0].Gw.String()
		log.Debug("route: multipath route observed; using first nexthop",
			"hop_count", len(r.MultiPath), "first_via", cur.Via)
	}
	linkIndex := r.LinkIndex
	if linkIndex == 0 && len(r.MultiPath) > 0 {
		linkIndex = r.MultiPath[0].LinkIndex
	}
	if linkIndex != 0 {
		link, err := netlink.LinkByIndex(linkIndex)
		if err != nil {
			log.Warn("route: LinkByIndex failed", "index", linkIndex, "err", err)
			return nil, fmt.Errorf("LinkByIndex(%d): %w", linkIndex, err)
		}
		cur.Dev = link.Attrs().Name
	}
	return cur, nil
}

// replaceTableDefaultNetlink installs (or replaces) a default route in the
// named table. Uses RouteReplace which is "add or update" in one syscall.
func replaceTableDefaultNetlink(
	ctx context.Context, log *slog.Logger, want RouteSpec,
) error {
	_ = ctx
	link, err := linkByName(log, want.Dev)
	if err != nil {
		return err
	}
	famConst := familyToNetlink(want.Family)

	gw := net.ParseIP(want.Via)
	if gw == nil {
		return fmt.Errorf("parse gateway %q: not a valid IP", want.Via)
	}

	r := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Table:     want.TableID,
		Gw:        gw,
		Family:    famConst,
		Priority:  want.Metric,
		Protocol:  netlink.RouteProtocol(want.Protocol),
	}
	// Dst nil means "default" for the family.

	start := realClock{}.Now()
	err = netlink.RouteReplace(r)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx, "route: RouteReplace (default)",
		"family", want.Family, "table_id", want.TableID,
		"via", want.Via, "dev", want.Dev,
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		log.WarnContext(ctx, "route: RouteReplace (default) failed",
			"family", want.Family, "table_id", want.TableID, "err", err)
		return fmt.Errorf("RouteReplace(default,%s,%d): %w", want.Family, want.TableID, err)
	}
	return nil
}

func replaceTableRouteNetlink(
	ctx context.Context, log *slog.Logger, want RouteSpec,
) error {
	_ = ctx
	link, err := linkByName(log, want.Dev)
	if err != nil {
		return err
	}
	route, err := buildTableRoute(log, want, link)
	if err != nil {
		return err
	}

	start := realClock{}.Now()
	err = netlink.RouteReplace(route)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteReplace (prefix)",
		"family", want.Family, "table_id", want.TableID,
		"dest", want.Dest, "via", want.Via, "dev", want.Dev,
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		log.WarnContext(ctx, "route: RouteReplace (prefix) failed",
			"family", want.Family, "table_id", want.TableID, "dest", want.Dest, "err", err)
		return fmt.Errorf("RouteReplace(prefix,%s,%d,%s): %w", want.Family, want.TableID, want.Dest, err)
	}
	return nil
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
		return nil, fmt.Errorf("parse gateway %q: not a valid IP", want.Via)
	}
	route.Gw = gateway
	return route, nil
}

// delTableDefaultNetlink removes the default route from the table. ENOENT
// (no such route) is swallowed and logged at DEBUG, matching the behaviour
// of the prior `ip route del` shellout which exited nonzero but the daemon
// treated as success when called via the wrapper.
func delTableDefaultNetlink(
	ctx context.Context, log *slog.Logger, family string, tableID int,
) error {
	_ = ctx
	famConst := familyToNetlink(family)

	r := &netlink.Route{
		Table:  tableID,
		Family: famConst,
		// Dst nil = default
	}
	start := realClock{}.Now()
	err := netlink.RouteDel(r)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteDel (default)",
		"family", family, "table_id", tableID,
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH)) {
		log.DebugContext(ctx, "route: nothing to delete (already absent)")
		return nil
	}
	if err != nil {
		log.WarnContext(ctx, "route: RouteDel (default) failed",
			"family", family, "table_id", tableID, "err", err)
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

	route := &netlink.Route{
		Table:    want.TableID,
		Dst:      destination,
		Family:   familyToNetlink(want.Family),
		Protocol: netlink.RouteProtocol(want.Protocol),
	}
	start := realClock{}.Now()
	err = netlink.RouteDel(route)
	duration := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteDel (prefix)",
		"family", want.Family, "table_id", want.TableID, "dest", want.Dest,
		"duration_ms", duration.Milliseconds(),
		"err", err,
	)
	if err != nil && (errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ESRCH)) {
		log.DebugContext(ctx, "route: nothing to delete (already absent)")
		return nil
	}
	if err != nil {
		log.WarnContext(ctx, "route: RouteDel (prefix) failed",
			"family", want.Family, "table_id", want.TableID, "dest", want.Dest, "err", err)
		return fmt.Errorf("RouteDel(prefix,%s,%d,%s): %w", want.Family, want.TableID, want.Dest, err)
	}
	return nil
}

// FindMainRADefault returns the RA-learned default route via iface in the
// main IPv6 routing table, if any. This is the source-of-truth gateway the
// daemon mirrors into the oob table.
func FindMainRADefault(
	ctx context.Context, iface string,
) (*CurrentRoute, error) {
	_ = ctx
	log := slog.Default().With("component", "route", "iface", iface, "op", "find-ra-default")
	log.DebugContext(ctx, "route: FindMainRADefault entry")

	link, err := linkByName(log, iface)
	if err != nil {
		return nil, err
	}

	filter := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Table:     unix.RT_TABLE_MAIN,
		Protocol:  unix.RTPROT_RA,
	}
	mask := netlink.RT_FILTER_OIF | netlink.RT_FILTER_TABLE | netlink.RT_FILTER_PROTOCOL

	start := realClock{}.Now()
	routes, err := netlink.RouteListFiltered(unix.AF_INET6, filter, mask)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteListFiltered (RA defaults)",
		"count", len(routes),
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		log.WarnContext(ctx, "route: RouteListFiltered (RA defaults) failed", "iface", iface, "err", err)
		return nil, fmt.Errorf("RouteListFiltered(ra-defaults,%s): %w", iface, err)
	}

	for _, r := range routes {
		if !isDefaultRoute(r, unix.AF_INET6) {
			continue
		}
		return routeToCurrent(log, r)
	}
	return nil, nil
}

// IfaceDefaultGateway returns the main-table default-route gateway for iface.
func IfaceDefaultGateway(family string, iface string) (string, error) {
	log := slog.Default().With(
		"component", "route", "iface", iface, "family", family, "op", "iface-default-gateway",
	)
	link, err := linkByName(log, iface)
	if err != nil {
		return "", err
	}

	famConst := familyToNetlink(family)
	filter := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Table:     unix.RT_TABLE_MAIN,
	}
	mask := netlink.RT_FILTER_OIF | netlink.RT_FILTER_TABLE

	start := realClock{}.Now()
	routes, err := netlink.RouteListFiltered(famConst, filter, mask)
	dur := realClock{}.Now().Sub(start)
	log.Debug(
		"route: RouteListFiltered (iface default gateway)",
		"count", len(routes),
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return "", nil
		}
		log.Warn("route: RouteListFiltered (iface default gateway) failed",
			"iface", iface, "family", family, "err", err)
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

// DeleteMainRADefaults removes every RA-learned IPv6 default route from the
// main table on iface. Returns the number of defaults deleted.
func DeleteMainRADefaults(
	ctx context.Context, log *slog.Logger, iface string,
) (int, error) {
	log = log.With("component", "route", "iface", iface, "op", "delete-ra-defaults")
	log.DebugContext(ctx, "route: DeleteMainRADefaults entry")

	link, err := linkByName(log, iface)
	if err != nil {
		return 0, err
	}

	filter := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Table:     unix.RT_TABLE_MAIN,
		Protocol:  unix.RTPROT_RA,
	}
	mask := netlink.RT_FILTER_OIF | netlink.RT_FILTER_TABLE | netlink.RT_FILTER_PROTOCOL

	start := realClock{}.Now()
	routes, err := netlink.RouteListFiltered(unix.AF_INET6, filter, mask)
	dur := realClock{}.Now().Sub(start)
	log.DebugContext(
		ctx,
		"route: RouteListFiltered (delete RA defaults)",
		"count", len(routes),
		"duration_ms", dur.Milliseconds(),
		"err", err,
	)
	if err != nil {
		log.WarnContext(ctx, "route: RouteListFiltered (delete RA defaults) failed", "iface", iface, "err", err)
		return 0, fmt.Errorf("RouteListFiltered(delete-ra-defaults,%s): %w", iface, err)
	}

	deletedCount := 0
	for _, route := range routes {
		if !isDefaultRoute(route, unix.AF_INET6) {
			continue
		}
		via := ""
		if route.Gw != nil {
			via = route.Gw.String()
		}
		route.Family = unix.AF_INET6
		delStart := realClock{}.Now()
		delErr := netlink.RouteDel(&route)
		delDur := realClock{}.Now().Sub(delStart)
		log.DebugContext(
			ctx,
			"route: RouteDel (RA default)",
			"via", via,
			"duration_ms", delDur.Milliseconds(),
			"err", delErr,
		)
		if delErr != nil {
			if errors.Is(delErr, syscall.ENOENT) || errors.Is(delErr, syscall.ESRCH) {
				continue
			}
			log.WarnContext(ctx, "route: RouteDel (RA default) failed", "via", via, "err", delErr)
			return deletedCount, fmt.Errorf("RouteDel(ra-default,%s): %w", via, delErr)
		}
		deletedCount++
	}
	return deletedCount, nil
}
