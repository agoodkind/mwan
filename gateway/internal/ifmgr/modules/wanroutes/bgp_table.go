package wanroutes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const bgpUnreachableMetric = 1024

// A lookup without a match in the provider table continues with the main
// table, where another provider's default route can match. The two routes
// cover every IPv6 destination. The provider table default reconciliation
// deletes default routes and does not delete these two routes.
func unreachableHalves() []string {
	return []string{"::/1", "8000::/1"}
}

func (m *Module) reconcileBGPTables(ctx context.Context, log *slog.Logger, current gateways, health netif.HealthStates, translations map[string]wanstate.MemberTranslation) error {
	var reconcileErr error
	for _, wan := range m.cfg.WANs {
		if !familyConfigured(wan, familyV6) {
			continue
		}
		wanGateways := current[wan.Key()]
		ready := wan.requiresBGP() && familyReady(wan, wanGateways, health, translations[wan.Key()], familyV6)
		err := reconcileBGPTable(ctx, log, wan, ready)
		if err == nil {
			err = setUnreachableRoutes(ctx, log, wan.TableID, ready && wanGateways.V6 == "")
		}
		if err != nil {
			m.excludeRouteFamily(current, wan.TableID, familyV6)
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("reconcile BGP routes table=%d: %w", wan.TableID, err))
		}
	}
	return reconcileErr
}

func bgpTableRouteKey(route netif.CurrentRoute) string {
	return fmt.Sprintf("%s metric %d via %s", route.Dest, route.Metric, route.Via)
}

func reconcileBGPTable(ctx context.Context, log *slog.Logger, wan WAN, ready bool) error {
	desired := make(map[string]bool)
	if ready {
		learned, err := netif.ListProtocolRoutes(ctx, log, familyV6, unix.RT_TABLE_MAIN, unix.RTPROT_BGP)
		if err != nil {
			log.WarnContext(ctx, "wan.routes: main-table BGP route read failed", "err", err)
			return fmt.Errorf("list main-table BGP routes: %w", err)
		}
		for _, route := range learned {
			if route.Dev != wan.Iface || route.Dest == "default" || !slices.Contains(wan.BGPRouteMetrics, route.Metric) {
				continue
			}
			copied := netif.RouteSpec{
				Family: familyV6, Dest: route.Dest, Via: route.Via, Dev: route.Dev,
				TableID: wan.TableID, Metric: route.Metric, Protocol: unix.RTPROT_BGP,
			}
			if err := netif.ReconcileTableRoute(ctx, log, copied); err != nil {
				log.WarnContext(ctx, "wan.routes: BGP route copy failed", "dest", route.Dest, "table_id", wan.TableID, "err", err)
				return fmt.Errorf("install %s: %w", route.Dest, err)
			}
			desired[bgpTableRouteKey(route)] = true
		}
	}
	installed, err := netif.ListProtocolRoutes(ctx, log, familyV6, wan.TableID, unix.RTPROT_BGP)
	if err != nil {
		log.WarnContext(ctx, "wan.routes: provider-table BGP route read failed", "table_id", wan.TableID, "err", err)
		return fmt.Errorf("list provider-table BGP routes: %w", err)
	}
	for _, route := range installed {
		if route.Type == unix.RTN_UNREACHABLE || desired[bgpTableRouteKey(route)] {
			continue
		}
		stale := netif.RouteSpec{
			Family: familyV6, Dest: route.Dest, Via: route.Via, Dev: route.Dev,
			TableID: wan.TableID, Metric: route.Metric, Protocol: unix.RTPROT_BGP,
		}
		if err := netif.DeleteTableRoute(ctx, log, stale); err != nil {
			log.WarnContext(ctx, "wan.routes: BGP route copy removal failed", "dest", route.Dest, "table_id", wan.TableID, "err", err)
			return fmt.Errorf("remove %s: %w", route.Dest, err)
		}
	}
	return nil
}

func setUnreachableRoutes(ctx context.Context, log *slog.Logger, tableID int, wanted bool) error {
	for _, half := range unreachableHalves() {
		_, destination, err := net.ParseCIDR(half)
		if err != nil {
			log.WarnContext(ctx, "wan.routes: unreachable route prefix invalid", "dest", half, "err", err)
			return fmt.Errorf("parse %s: %w", half, err)
		}
		route := &netlink.Route{
			Family: netlink.FAMILY_V6, Dst: destination, Table: tableID, Type: unix.RTN_UNREACHABLE,
			Priority: bgpUnreachableMetric, Protocol: unix.RTPROT_BGP,
		}
		if wanted {
			err = netlink.RouteReplace(route)
		} else {
			err = netlink.RouteDel(route)
			if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
				err = nil
			}
		}
		if err != nil {
			log.WarnContext(ctx, "wan.routes: unreachable route write failed", "dest", half, "wanted", wanted, "err", err)
			return fmt.Errorf("set unreachable route %s wanted=%t: %w", half, wanted, err)
		}
	}
	return nil
}
