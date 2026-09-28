package agent

import (
	"context"
	"fmt"
	"time"

	"goodkind.io/mwan/internal/forwardingready"
)

const forwardingPollInterval = time.Second
const forwardingReadTimeout = 500 * time.Millisecond

// SetForwardingReady requires current ifmgr readiness before this agent
// announces the primary's configured address families.
func (a *Server) SetForwardingReady(path string, ipv4, ipv6 bool) {
	a.forwardingMu.Lock()
	defer a.forwardingMu.Unlock()
	a.forwardingPath = path
	a.announceIPv4 = ipv4
	a.announceIPv6 = ipv6
}

func (a *Server) readForwardingReady(ctx context.Context) (forwardingready.State, error) {
	readCtx, cancel := context.WithTimeout(ctx, forwardingReadTimeout)
	defer cancel()
	state, err := forwardingready.Read(readCtx, a.forwardingPath)
	if err != nil {
		if err.Error() != a.lastReadError {
			a.log.WarnContext(ctx, "read primary forwarding readiness failed", "error", err)
			a.lastReadError = err.Error()
		}
		return forwardingready.State{}, fmt.Errorf("read primary forwarding readiness: %w", err)
	}
	a.lastReadError = ""
	return state, nil
}

func (a *Server) pollForwardingReady(ctx context.Context) {
	ticker := time.NewTicker(forwardingPollInterval)
	defer ticker.Stop()
	for {
		a.reconcileForwardingReady(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Server) reconcileForwardingReady(ctx context.Context) {
	a.forwardingMu.Lock()
	defer a.forwardingMu.Unlock()
	if a.manualWithdraw || a.bgp == nil || a.forwardingPath == "" {
		return
	}
	state, err := a.readForwardingReady(ctx)
	if err != nil {
		state = forwardingready.State{}
	}
	if err := a.bgp.SetDefaultAnnouncements(state.IPv4, state.IPv6); err != nil {
		a.log.ErrorContext(ctx, "reconcile primary BGP announcements failed", "error", err)
	}
}
