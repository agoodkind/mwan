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

// ForwardingReadinessReporter supplies family readiness for roles that do not use WAN routing state.
type ForwardingReadinessReporter interface {
	ForwardingReadiness() forwardingready.State
}

func (d *Daemon) reportedForwardingReadiness() forwardingready.State {
	for _, module := range d.modules {
		if reporter, ok := module.(ForwardingReadinessReporter); ok {
			return reporter.ForwardingReadiness()
		}
	}
	return forwardingready.State{IPv4: false, IPv6: false}
}

func forwardingFailureImpact(module Module) forwardingready.State {
	if reporter, ok := module.(forwardingFailureReporter); ok {
		return reporter.ForwardingFailureImpact()
	}
	return forwardingready.State{IPv4: true, IPv6: true}
}
