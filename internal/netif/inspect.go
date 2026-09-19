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

// LinkState is the current operational and carrier state of one interface.
type LinkState struct {
	OperState string
	Carrier   bool
}

// LinkStats is the current receive and transmit counters for one interface.
type LinkStats struct {
	RxBytes   uint64
	RxPackets uint64
	RxErrors  uint64
	RxDropped uint64
	TxBytes   uint64
	TxPackets uint64
	TxErrors  uint64
	TxDropped uint64
}

// RouteLookupResult is the selected output interface, gateway, and source address.
type RouteLookupResult struct {
	OIF     string
	Gateway string
	Source  string
}

// ListRules returns the current policy rules for one address family.
func ListRules(ctx context.Context, log *slog.Logger, family string) ([]CurrentRule, error) {
	log = log.With("component", "rules", "op", "list", "family", family)
	rules, err := listRulesNetlink(log, family)
	if err != nil {
		log.WarnContext(ctx, "rules: list failed", "err", err)
		return nil, fmt.Errorf("list %s rules: %w", family, err)
	}
	return rules, nil
}

// ListDHCPRoutes returns routes installed with the DHCP protocol for one family.
func ListDHCPRoutes(
	ctx context.Context,
	log *slog.Logger,
	family string,
) ([]CurrentRoute, error) {
	filter := &netlink.Route{Protocol: unix.RTPROT_DHCP}
	return listRoutesFiltered(
		ctx,
		log,
		family,
		filter,
		netlink.RT_FILTER_PROTOCOL,
		"dhcp",
	)
}

// ListProtocolRoutes returns routes installed with protocol in tableID for one family.
func ListProtocolRoutes(
	ctx context.Context,
	log *slog.Logger,
	family string,
	tableID int,
	protocol int,
) ([]CurrentRoute, error) {
	filter := &netlink.Route{Table: tableID, Protocol: netlink.RouteProtocol(protocol)}
	return listRoutesFiltered(
		ctx,
		log,
		family,
		filter,
		netlink.RT_FILTER_TABLE|netlink.RT_FILTER_PROTOCOL,
		fmt.Sprintf("table-%d-protocol-%d", tableID, protocol),
	)
}

// ListTableRoutes returns all routes in tableID for one address family.
func ListTableRoutes(
	ctx context.Context,
	log *slog.Logger,
	family string,
	tableID int,
) ([]CurrentRoute, error) {
	filter := &netlink.Route{Table: tableID}
	return listRoutesFiltered(
		ctx,
		log,
		family,
		filter,
		netlink.RT_FILTER_TABLE,
		fmt.Sprintf("table-%d", tableID),
	)
}

func listRoutesFiltered(
	ctx context.Context,
	log *slog.Logger,
	family string,
	filter *netlink.Route,
	filterMask uint64,
	filterName string,
) ([]CurrentRoute, error) {
	log = log.With(
		"component", "route",
		"op", "list",
		"family", family,
		"filter", filterName,
	)
	startTime := realClock{}.Now()
	routes, err := netlink.RouteListFiltered(familyToNetlink(family), filter, filterMask)
	duration := realClock{}.Now().Sub(startTime)
	log.DebugContext(
		ctx,
		"route: RouteListFiltered",
		"count", len(routes),
		"duration_ms", duration.Milliseconds(),
		"err", err,
	)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			return []CurrentRoute{}, nil
		}
		log.WarnContext(ctx, "route: RouteListFiltered failed", "err", err)
		return nil, fmt.Errorf("RouteListFiltered(%s,%s): %w", family, filterName, err)
	}

	current := make([]CurrentRoute, 0, len(routes))
	for _, route := range routes {
		converted, err := routeToCurrent(log, route)
		if err != nil {
			return nil, fmt.Errorf("routeToCurrent(%s,%s): %w", family, filterName, err)
		}
		current = append(current, *converted)
	}
	return current, nil
}

// ReadLinkState returns the current operational and carrier state for iface.
func ReadLinkState(log *slog.Logger, iface string) (LinkState, error) {
	link, err := linkByName(log, iface)
	if err != nil {
		return LinkState{}, err
	}
	attrs := link.Attrs()
	return LinkState{
		OperState: attrs.OperState.String(),
		Carrier:   attrs.RawFlags&unix.IFF_LOWER_UP != 0,
	}, nil
}

// ReadLinkStats returns the current receive and transmit counters for iface.
func ReadLinkStats(log *slog.Logger, iface string) (LinkStats, error) {
	link, err := linkByName(log, iface)
	if err != nil {
		return LinkStats{}, err
	}
	statistics := link.Attrs().Statistics
	if statistics == nil {
		return LinkStats{}, fmt.Errorf("link %q has no statistics", iface)
	}
	return LinkStats{
		RxBytes:   statistics.RxBytes,
		RxPackets: statistics.RxPackets,
		RxErrors:  statistics.RxErrors,
		RxDropped: statistics.RxDropped,
		TxBytes:   statistics.TxBytes,
		TxPackets: statistics.TxPackets,
		TxErrors:  statistics.TxErrors,
		TxDropped: statistics.TxDropped,
	}, nil
}

// RouteLookup resolves a route for target, source, and fwmark in one address
// family. ok is false when the kernel returns no route, which includes an
// unreachable answer; that is a normal negative outcome, not an error, so a
// probe caller can report it and keep going. err is reserved for real failures
// such as an unparseable address or a link lookup that fails.
func RouteLookup(
	ctx context.Context,
	log *slog.Logger,
	family string,
	target string,
	source string,
	fwmark uint32,
) (RouteLookupResult, bool, error) {
	targetIP, err := parseFamilyIP(family, target)
	if err != nil {
		return RouteLookupResult{}, false, fmt.Errorf("target: %w", err)
	}
	sourceIP, err := parseFamilyIP(family, source)
	if err != nil {
		return RouteLookupResult{}, false, fmt.Errorf("source: %w", err)
	}

	startTime := realClock{}.Now()
	routes, err := netlink.RouteGetWithOptions(targetIP, &netlink.RouteGetOptions{
		Iif:      "",
		IifIndex: 0,
		Oif:      "",
		OifIndex: 0,
		VrfName:  "",
		SrcAddr:  sourceIP,
		UID:      nil,
		Mark:     fwmark,
		FIBMatch: false,
	})
	duration := realClock{}.Now().Sub(startTime)
	log.DebugContext(
		ctx,
		"route: RouteGetWithOptions",
		"family", family,
		"target", target,
		"source", source,
		"fwmark", fwmark,
		"count", len(routes),
		"duration_ms", duration.Milliseconds(),
		"err", err,
	)
	if err != nil {
		if isNoRouteError(err) {
			return RouteLookupResult{OIF: "", Gateway: "", Source: ""}, false, nil
		}
		log.WarnContext(ctx, "route: RouteGetWithOptions failed", "err", err)
		return RouteLookupResult{}, false, fmt.Errorf(
			"RouteGetWithOptions(%s,%s,mark=%d): %w",
			target,
			source,
			fwmark,
			err,
		)
	}
	if len(routes) == 0 {
		return RouteLookupResult{OIF: "", Gateway: "", Source: ""}, false, nil
	}
	result, err := routeLookupResult(log, routes[0], source)
	if err != nil {
		return RouteLookupResult{}, false, err
	}
	return result, true, nil
}

// isNoRouteError reports whether err is the kernel telling us there is no route
// to the target, rather than an operational failure.
func isNoRouteError(err error) bool {
	return errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETDOWN)
}

func parseFamilyIP(family string, rawIP string) (net.IP, error) {
	ip := net.ParseIP(rawIP)
	if ip == nil {
		return nil, fmt.Errorf("parse %q: not an IP address", rawIP)
	}
	if family == "inet" {
		ip = ip.To4()
		if ip == nil {
			return nil, fmt.Errorf("parse %q: not an IPv4 address", rawIP)
		}
		return ip, nil
	}
	if family != "inet6" || ip.To4() != nil {
		return nil, fmt.Errorf("parse %q: not an IPv6 address", rawIP)
	}
	return ip, nil
}

func routeLookupResult(
	log *slog.Logger,
	route netlink.Route,
	requestedSource string,
) (RouteLookupResult, error) {
	linkIndex := route.LinkIndex
	gateway := route.Gw
	if len(route.MultiPath) > 0 {
		if linkIndex == 0 {
			linkIndex = route.MultiPath[0].LinkIndex
		}
		if gateway == nil {
			gateway = route.MultiPath[0].Gw
		}
	}

	oif := ""
	if linkIndex != 0 {
		link, err := netlink.LinkByIndex(linkIndex)
		if err != nil {
			log.Warn("route: LinkByIndex failed", "index", linkIndex, "err", err)
			return RouteLookupResult{}, fmt.Errorf("LinkByIndex(%d): %w", linkIndex, err)
		}
		oif = link.Attrs().Name
	}
	source := requestedSource
	if route.Src != nil {
		source = route.Src.String()
	}
	gatewayString := ""
	if gateway != nil {
		gatewayString = gateway.String()
	}
	return RouteLookupResult{OIF: oif, Gateway: gatewayString, Source: source}, nil
}
