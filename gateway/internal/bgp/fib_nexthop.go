package bgp

import (
	"context"
	"errors"
	"fmt"
	"net/netip"

	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
)

type nextHopMatch struct {
	route netif.CurrentRoute
	found bool
	err   error
}

func (f *FIB) gateway(ctx context.Context, prefix netip.Prefix, nextHop netip.Addr, matches map[netip.Addr]nextHopMatch) (string, error) {
	if !f.cfg.ResolveNextHop {
		return nextHop.String(), nil
	}
	match, cached := matches[nextHop]
	if !cached {
		match.route, match.found, match.err = netif.RouteMatch(ctx, f.log, prefixFamily(prefix), nextHop.String(), f.cfg.InternalIface)
		matches[nextHop] = match
	}
	if match.err != nil {
		f.log.ErrorContext(ctx, "resolve BGP next hop", "err", match.err, "prefix", prefix, "next_hop", nextHop)
		return "", fmt.Errorf("resolve BGP next hop %s: %w", nextHop, match.err)
	}
	if !match.found || match.route.Via == "" {
		return nextHop.String(), nil
	}
	destination := match.route.Dest
	if destination == listedDefaultRoute {
		destination = defaultPrefix(prefixFamily(prefix))
	}
	installed := f.desired[prefix.String()].gateway
	own := destination == prefix.String() && match.route.Metric == int(f.cfg.Metric) && match.route.Protocol == unix.RTPROT_BGP
	if own && installed != "" {
		return installed, nil
	}
	return match.route.Via, nil
}

func (f *FIB) installedCounts(ctx context.Context) (map[string]int, error) {
	counts := make(map[string]int)
	for _, tableID := range f.cfg.Tables {
		for _, family := range [...]string{ipv4RouteFamily, ipv6RouteFamily} {
			routes, err := f.writer.ListProtocolRoutes(ctx, f.log, family, tableID, unix.RTPROT_BGP)
			if err != nil {
				f.log.ErrorContext(ctx, "list BGP table routes", "err", err, "table_id", tableID)
				return nil, fmt.Errorf("list table %d BGP routes: %w", tableID, err)
			}
			for _, route := range routes {
				if route.Dev != f.cfg.InternalIface || route.Metric != int(f.cfg.Metric) {
					continue
				}
				if route.Dest == listedDefaultRoute {
					route.Dest = defaultPrefix(family)
				}
				counts[route.Dest]++
			}
		}
	}
	return counts, nil
}

func kernelRejected(err error) bool {
	return errors.Is(err, unix.EHOSTUNREACH) || errors.Is(err, unix.ENETUNREACH) || errors.Is(err, unix.EINVAL)
}

// Refresh reinstalls each desired route that is absent from the kernel or uses another gateway than the kernel route to the BGP next hop, and returns the prefixes with an installation that the kernel rejected.
func (f *FIB) Refresh(ctx context.Context) (map[netip.Prefix]error, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rejected := make(map[netip.Prefix]error)
	if !f.cfg.ResolveNextHop || len(f.desired) == 0 {
		return rejected, nil
	}
	installed, err := f.installedCounts(ctx)
	if err != nil {
		return nil, err
	}
	matches := make(map[netip.Addr]nextHopMatch)
	var refreshErr error
	for key, desired := range f.desired {
		prefix, parseErr := netip.ParsePrefix(key)
		if parseErr != nil {
			continue
		}
		gateway, err := f.gateway(ctx, prefix, desired.nextHop, matches)
		if err != nil {
			refreshErr = errors.Join(refreshErr, err)
			continue
		}
		if gateway == desired.gateway && installed[key] == len(f.cfg.Tables) {
			continue
		}
		f.log.DebugContext(ctx, "reinstall BGP route", "prefix", prefix, "previous", desired.gateway, "gateway", gateway, "tables", installed[key])
		installErr := f.installGateway(ctx, prefix, desired.nextHop, gateway)
		if installErr != nil && kernelRejected(installErr) {
			rejected[prefix] = installErr
		} else if installErr != nil {
			refreshErr = errors.Join(refreshErr, installErr)
		}
	}
	return rejected, refreshErr
}
