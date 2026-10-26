package netif

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/agoodkind/netlink"
)

// RouteMatch returns the kernel route that forwards target through dev and reports false when no route through dev matches.
func RouteMatch(ctx context.Context, log *slog.Logger, family, target, dev string) (CurrentRoute, bool, error) {
	var none CurrentRoute
	targetIP, err := parseFamilyIP(family, target)
	if err != nil {
		return none, false, fmt.Errorf("target: %w", err)
	}
	routes, err := netlink.RouteGetWithOptions(targetIP, &netlink.RouteGetOptions{
		Iif:      "",
		IifIndex: 0,
		Oif:      dev,
		OifIndex: 0,
		VrfName:  "",
		SrcAddr:  nil,
		UID:      nil,
		Mark:     0,
		FIBMatch: true,
	})
	log.DebugContext(ctx, "route: RouteGetWithOptions", "family", family, "target", target, "dev", dev, "count", len(routes), "err", err)
	if err != nil {
		if isNoRouteError(err) {
			return none, false, nil
		}
		log.WarnContext(ctx, "route: route match lookup failed", "target", target, "dev", dev, "err", err)
		return none, false, fmt.Errorf("RouteGetWithOptions(%s,oif=%s): %w", target, dev, err)
	}
	if len(routes) == 0 {
		return none, false, nil
	}
	matched, err := routeToCurrent(log, routes[0])
	if err != nil {
		return none, false, err
	}
	return *matched, true, nil
}
