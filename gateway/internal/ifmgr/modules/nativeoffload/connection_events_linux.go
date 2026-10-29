//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"syscall"

	"github.com/ti-mo/conntrack"

	"goodkind.io/mwan/internal/offload"
)

func (module *Module) observeConnection(ctx context.Context, event offload.ConntrackEvent) {
	identity := flowIdentity(event.Flow)
	if event.Destroyed {
		module.destroyConnection(ctx, identity, event)
		return
	}
	entry, installed := module.flows[identity]
	if !installed || sameConnectionPolicy(entry.flow, event.Flow) {
		return
	}
	// Removal retains the restoration handle while the notification receives
	// a fresh kernel check. An update is not a complete current snapshot.
	if err := module.retire(ctx, identity, "connection notification requires verification"); err != nil {
		_ = module.fail(ctx, err)
		return
	}
	requestContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.RequestTimeout)
	defer cancel()
	evidence := module.evidenceFor(module.currentEvidenceEpoch())
	_, decision, err := module.verifyConnectionUpdate(requestContext, event, entry, evidence)
	if err != nil || decision != EligibilityReady {
		return
	}
	module.flows[identity] = entry
	delete(module.resumeCandidates, identity)
	module.resumeEntry(ctx, requestContext, identity, entry, evidence)
}

func (module *Module) destroyConnection(ctx context.Context, identity connectionIdentity, event offload.ConntrackEvent) {
	entry, installed := module.flows[identity]
	if installed {
		if err := module.retire(ctx, identity, "connection destroyed"); err != nil {
			_ = module.fail(ctx, err)
			return
		}
	} else {
		entry = module.resumeCandidates[identity]
	}
	if entry == nil {
		return
	}
	if entry.uncertain || entry.result.CleanupFailedDirections != 0 || entry.result.InstalledDirections != 0 {
		module.ownershipUncertain = true
		_ = module.fail(ctx, fmt.Errorf("destroyed connection %d retains uncertain native cleanup", identity.id))
		return
	}
	delete(module.resumeCandidates, identity)
	module.Log.InfoContext(ctx, "native conntrack destruction confirmed", "connection_id", identity.id,
		"handle", entry.result.Handle, "event_received_at", event.ReceivedAt,
		"trigger", conntrackPolicyValue(event.Flow))
}

func (module *Module) verifyConnectionUpdate(ctx context.Context, event offload.ConntrackEvent, entry *admittedFlow, evidence *evidenceCheck) (current conntrack.Flow, decision Eligibility, resultErr error) {
	decision = EligibilityStale
	defer func() {
		log := module.Log.With("connection_id", entry.flow.ID, "handle", entry.result.Handle,
			"event_received_at", event.ReceivedAt, "event_destroyed", event.Destroyed,
			"trigger", conntrackPolicyValue(event.Flow), "fresh", conntrackPolicyValue(current), "decision", decision)
		if resultErr != nil {
			log.WarnContext(ctx, "native conntrack update verification paused", "err", resultErr)
		} else {
			log.InfoContext(ctx, "native conntrack update verified")
		}
	}()
	var err error
	current, err = offload.FreshConntrackFlow(ctx, entry.flow)
	if err != nil {
		if errors.Is(err, syscall.ENOENT) {
			decision = EligibilityUnsupported
		}
		return current, decision, fmt.Errorf("verify current native connection: %w", err)
	}
	if !sameConnectionPolicy(entry.flow, current) {
		return current, EligibilityUnsupported, nil
	}
	spec, guards, confirmed, wan, eligibility, err := module.candidate(ctx, entry.observation)
	if err != nil {
		return current, EligibilityStale, fmt.Errorf("verify current native connection mapping: %w", err)
	}
	if confirmed.ID != 0 {
		current = confirmed
	}
	if eligibility != EligibilityReady {
		return current, eligibility, nil
	}
	if !sameConnectionPolicy(entry.flow, current) || !sameNativeMapping(entry.spec, spec) || guards != entry.guards || wan.Key() != entry.wan {
		return current, EligibilityUnsupported, nil
	}
	if err := evidence.guard.Check(); err != nil {
		return current, EligibilityStale, fmt.Errorf("verify native connection forwarding evidence: %w", err)
	}
	return current, EligibilityReady, nil
}

func conntrackPolicyValue(flow conntrack.Flow) slog.Value {
	fields := []slog.Attr{
		slog.Uint64("id", uint64(flow.ID)), slog.Uint64("mark", uint64(flow.Mark)),
		slog.Uint64("status", uint64(flow.Status)), slog.Uint64("timeout", uint64(flow.Timeout)),
		slog.Bool("tcp_present", flow.ProtoInfo.TCP != nil),
	}
	if flow.ProtoInfo.TCP != nil {
		fields = append(fields, slog.Int("tcp_state", int(flow.ProtoInfo.TCP.State)))
	}
	return slog.GroupValue(fields...)
}
