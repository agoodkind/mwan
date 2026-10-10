package wanroutes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

func endpointDestination(wan WAN) string {
	return netip.PrefixFrom(wan.Tunnel.Remote, wan.Tunnel.Remote.BitLen()).String()
}

// The map key is the tunnel's connection ID. A tunnel has no route while the underlay provider has no IPv4 gateway.
func endpointRoutes(cfg Config, current gateways) map[string]netif.RouteSpec {
	routes := make(map[string]netif.RouteSpec)
	for _, wan := range cfg.WANs {
		underlay, found := tunnelUnderlay(cfg, wan)
		if !found {
			continue
		}
		gateway := current[underlay.Key()].V4
		if gateway == "" {
			continue
		}
		routes[wan.Key()] = netif.RouteSpec{
			Family: familyV4, Dest: endpointDestination(wan), Via: gateway, Dev: underlay.Iface,
			TableID: underlay.TableID, Metric: 0, Protocol: netif.TunnelEndpointRouteProtocol,
		}
	}
	return routes
}

func (m *Module) endpointJournal() ifmgr.TunnelEndpointRoutes {
	if m.Env == nil {
		return nil
	}
	return m.Env.TunnelEndpointRoutes
}

// The routing specification requires each tunnel's endpoint route in the underlay provider's table.
// The sit device also restricts the outer route lookup to routes through the underlay interface.
// The journal records each installed route. Cleanup deletes only routes recorded in the journal.
// The module does not read a kernel route when the configuration has no tunnel and no recorded route.
// Callers must lock the module.
func (m *Module) reconcileEndpointRoutes(ctx context.Context, log *slog.Logger, current gateways) error {
	desired := endpointRoutes(m.cfg, current)
	installed := make(map[string]netif.RouteSpec, len(desired))
	watched := make(map[string]netif.RouteSpec, len(desired))
	previous := m.endpointWatch
	m.endpointRoutes, m.endpointWatch = installed, watched
	journal := m.endpointJournal()
	if journal == nil {
		if len(desired) == 0 {
			return nil
		}
		return fmt.Errorf("tunnel endpoint routes require the address ownership journal")
	}
	var reconcileErr error
	keep := make(map[string]bool, len(desired))
	for key, want := range desired {
		// The release step does not delete the recorded route of the same tunnel after a failed write.
		keep[key] = true
		if err := journal.EnsureTunnelEndpointRoute(ctx, log, key, want); err != nil {
			log.WarnContext(ctx, "wan.routes: tunnel endpoint route write failed", "table_id", want.TableID, "dest", want.Dest, "err", err)
			reconcileErr = errors.Join(reconcileErr, fmt.Errorf("install tunnel endpoint route table=%d dest=%s: %w", want.TableID, want.Dest, err))
			// The route of an earlier pass can still exist in the kernel. A deletion of that route by
			// another writer must request a repair pass.
			if last, known := previous[key]; known {
				watched[key] = last
			}
			continue
		}
		installed[key] = want
		watched[key] = want
	}
	if err := journal.ReleaseTunnelEndpointRoutes(ctx, log, keep); err != nil {
		log.WarnContext(ctx, "wan.routes: tunnel endpoint route removal failed", "err", err)
		reconcileErr = errors.Join(reconcileErr, fmt.Errorf("release tunnel endpoint routes: %w", err))
	}
	return reconcileErr
}

// A deletion event matches the routes installed in the last pass.
// A deletion event also matches each tunnel's last installed route when the tunnel had a failed write
// in the last pass.
// The module's own deletion does not match an entry after a pass without that route.
func (m *Module) ownsEndpointRouteDeletion(event netif.Event) bool {
	if event.Family != familyV4 || event.Protocol != netif.TunnelEndpointRouteProtocol {
		return false
	}
	m.Lock()
	defer m.Unlock()
	for _, installed := range m.endpointWatch {
		if event.Iface == installed.Dev && event.TableID == installed.TableID && event.Dest == installed.Dest &&
			event.Via == installed.Via && event.Metric == installed.Metric {
			return true
		}
	}
	return false
}
