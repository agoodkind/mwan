package agent

import (
	"context"
	"fmt"
	"time"

	"goodkind.io/mwan/internal/forwardingready"
)

// SetForwardingReady configures polling of ifmgr readiness for primary announcements.
func (a *Server) SetForwardingReady(path string, pollInterval, readTimeout time.Duration, ipv4, ipv6 bool) {
	a.forwardingMu.Lock()
	defer a.forwardingMu.Unlock()
	a.forwardingPath = path
	a.forwardingPollInterval = pollInterval
	a.forwardingReadTimeout = readTimeout
	a.announceIPv4 = ipv4
	a.announceIPv6 = ipv6
}

func (a *Server) readForwardingReady(ctx context.Context) (forwardingready.State, error) {
	readCtx, cancel := context.WithTimeout(ctx, a.forwardingReadTimeout)
	defer cancel()
	state, err := forwardingready.Read(readCtx, a.forwardingPath, a.forwardingReadTimeout)
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
	ticker := time.NewTicker(a.forwardingPollInterval)
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
