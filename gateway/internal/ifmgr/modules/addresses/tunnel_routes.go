package addresses

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/netif"
)

// EnsureTunnelEndpointRoute also fences callers outside WAN reconciliation.
func (module *Module) EnsureTunnelEndpointRoute(ctx context.Context, log *slog.Logger, connectionID string, want netif.RouteSpec) (resultErr error) {
	defer func() {
		if resultErr != nil {
			log.WarnContext(ctx, "addresses: tunnel endpoint route change failed", "connection_id", connectionID, "err", resultErr)
		}
	}()
	ctx, finish, err := module.Env.BeginForwardingChange(ctx)
	if err != nil {
		return fmt.Errorf("prepare tunnel endpoint route change: %w", err)
	}
	defer finish()
	if err := module.reconciler.EnsureTunnelEndpointRoute(ctx, log, connectionID, want); err != nil {
		return fmt.Errorf("write tunnel endpoint route: %w", err)
	}
	return nil
}

// ReleaseTunnelEndpointRoutes keeps route removal inside the same guard contract.
func (module *Module) ReleaseTunnelEndpointRoutes(ctx context.Context, log *slog.Logger, keep map[string]bool) (resultErr error) {
	defer func() {
		if resultErr != nil {
			log.WarnContext(ctx, "addresses: tunnel endpoint route removal failed", "err", resultErr)
		}
	}()
	ctx, finish, err := module.Env.BeginForwardingChange(ctx)
	if err != nil {
		return fmt.Errorf("prepare tunnel endpoint route removal: %w", err)
	}
	defer finish()
	if err := module.reconciler.ReleaseTunnelEndpointRoutes(ctx, log, keep); err != nil {
		return fmt.Errorf("remove tunnel endpoint routes: %w", err)
	}
	return nil
}
