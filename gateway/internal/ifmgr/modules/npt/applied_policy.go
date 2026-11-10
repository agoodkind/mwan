package npt

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/nptpolicy"
)

func (m *Module) beginPolicyChange(ctx context.Context, log *slog.Logger, pairs map[string]nptpolicy.PrefixPair) ([]string, func(), error) {
	if m.Env == nil || m.Env.LiveState == nil {
		return nil, nil, nil
	}
	changed, finish, err := m.Env.LiveState.BeginNPTChange(ctx, pairs)
	if err != nil {
		log.WarnContext(ctx, "npt: policy change guard failed", "connections", changed, "err", err)
		return changed, nil, fmt.Errorf("guard NPT policy change: %w", err)
	}
	return changed, finish, nil
}

func (m *Module) finishPolicyChange(changed []string, finish func(), published *bool) {
	if finish == nil {
		return
	}
	if !*published {
		m.failAppliedPolicy(changed)
	}
	finish()
}

func (m *Module) failAppliedPolicy(changed []string) {
	if m.Env == nil || m.Env.LiveState == nil || len(changed) == 0 {
		return
	}
	snapshot := m.Env.LiveState.Snapshot()
	for _, id := range changed {
		delete(snapshot.NPTPolicies, id)
		member := snapshot.Translation[id]
		member.V6.Ready = false
		member.V6.Reason = "NPT policy mutation did not complete"
		snapshot.Translation[id] = member
	}
	if m.Env.LiveState.SetNPTTranslation(snapshot.Translation, snapshot.NPTPolicies) && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile("NPT applied policy became unavailable")
	}
}

func (m *Module) publishPreparedPolicy(iface string, pairs map[string]nptpolicy.PrefixPair, changed []string) {
	if m.Env == nil || m.Env.LiveState == nil {
		return
	}
	snapshot := m.Env.LiveState.Snapshot()
	for _, id := range changed {
		previous, applied := snapshot.NPTPolicies[id]
		next, desired := pairs[id]
		member := snapshot.Translation[id]
		sameInterface := false
		for _, wan := range m.cfg.WANs {
			sameInterface = sameInterface || wan.Key() == id && wan.Iface == iface
		}
		if applied && desired && sameInterface && member.V6.Ready && samePreparedPolicyBase(previous, next) {
			snapshot.NPTPolicies[id] = next
			continue
		}
		delete(snapshot.NPTPolicies, id)
		member.V6.Ready = false
		member.V6.Reason = "NPT policy requires complete reconciliation"
		snapshot.Translation[id] = member
	}
	if m.Env.LiveState.SetNPTTranslation(snapshot.Translation, snapshot.NPTPolicies) && m.Env.RequestReconcile != nil {
		m.Env.RequestReconcile("NPT applied exceptions changed")
	}
}

func samePreparedPolicyBase(previous, next nptpolicy.PrefixPair) bool {
	previous.DestinationExceptions = nil
	next.DestinationExceptions = nil
	return nptpolicy.EqualPair(previous, next)
}
