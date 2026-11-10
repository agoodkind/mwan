package wanroutes

import (
	"context"
	"log/slog"

	"goodkind.io/mwan/internal/netif"
)

func (m *Module) onRuleEvent(ctx context.Context, log *slog.Logger, event netif.RuleEvent) {
	if event.Resync {
		m.requestRepair("policy rule monitor resubscribed")
		return
	}
	if !m.ownsDesiredRuleDeletion(ctx, log, event) {
		return
	}
	log.WarnContext(ctx, "wan.routes: owned policy rule removed",
		"family", event.Family, "table_id", event.TableID, "priority", event.Priority)
	m.requestRepair("owned policy rule deleted")
}
