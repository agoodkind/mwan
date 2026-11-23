package netif

import (
	"context"
	"time"

	"github.com/vishvananda/netlink"
)

func sleepMonitorRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func routeUsesIndex(route netlink.Route, index int) bool {
	if route.LinkIndex == index {
		return true
	}

	for _, hop := range route.MultiPath {
		if hop != nil && hop.LinkIndex == index {
			return true
		}
	}

	return false
}

func observedRouteNextHops(route netlink.Route) []RouteNextHop {
	hops := make([]RouteNextHop, 0, len(route.MultiPath)+1)
	if route.LinkIndex != 0 {
		var hop RouteNextHop
		hop.LinkIndex = route.LinkIndex
		hop.Weight = 1
		if route.Gw != nil {
			hop.Via = route.Gw.String()
		}

		hops = append(hops, hop)
	}

	for _, next := range route.MultiPath {
		if next == nil {
			continue
		}

		var hop RouteNextHop
		hop.LinkIndex = next.LinkIndex
		hop.Weight = next.Hops + 1
		if next.Gw != nil {
			hop.Via = next.Gw.String()
		}

		hops = append(hops, hop)
	}

	for i := range hops {
		link, err := netlink.LinkByIndex(hops[i].LinkIndex)
		if err == nil && link.Attrs() != nil {
			hops[i].Dev = link.Attrs().Name
		}
	}

	return hops
}

func unknownEvent() Event {
	var event Event
	event.Kind = EvUnknown

	return event
}

func sleepOrCancel(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
