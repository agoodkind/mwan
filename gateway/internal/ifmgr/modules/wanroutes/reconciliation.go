package wanroutes

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

func (m *Module) reconcileFallbackRules(
	ctx context.Context,
	log *slog.Logger,
	currentGateways gateways,
	rules []netif.DesiredRule,
	reconcileErr *error,
) {
	for _, rule := range rules {
		if rule.Priority != catchAllPriority {
			continue
		}

		if err := netif.ReconcileRules(ctx, log, []netif.DesiredRule{rule}); err != nil {
			for _, wan := range m.cfg.WANs {
				m.excludeRouteFamily(currentGateways, wan.TableID, rule.Family)
			}

			*reconcileErr = errors.Join(
				*reconcileErr,
				fmt.Errorf("reconcile fallback rule: %w", err),
			)
		}
	}
}

func (m *Module) completeRoutePass(
	ctx context.Context,
	log *slog.Logger,
	currentGateways gateways,
	health netif.HealthStates,
	translations map[string]wanstate.MemberTranslation,
) {
	m.checkNextHopLocked(ctx, log)
	m.publishLiveState(currentGateways, health, translations)
}
