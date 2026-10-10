package ifmgr

import (
	"context"
	"log/slog"

	"goodkind.io/mwan/internal/forwardingready"
)

// Modules without this contract invalidate both families.
type forwardingFailureReporter interface {
	ForwardingFailureImpact() forwardingready.State
}

// FamilyReadinessObserver is an optional Module extension that the daemon calls after each wan-role pass publishes the forwarding readiness of every connection.
type FamilyReadinessObserver interface {
	OnFamilyReadiness(ctx context.Context, log *slog.Logger)
}

func forwardingFailureImpact(module Module) forwardingready.State {
	if reporter, ok := module.(forwardingFailureReporter); ok {
		return reporter.ForwardingFailureImpact()
	}
	return forwardingready.State{IPv4: true, IPv6: true}
}
