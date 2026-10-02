package ifmgr

import "goodkind.io/mwan/internal/forwardingready"

// ForwardingFailureReporter limits a reconciliation failure to affected families.
// Modules without this contract invalidate both families.
type ForwardingFailureReporter interface {
	ForwardingFailureImpact() forwardingready.State
}

func forwardingFailureImpact(module Module) forwardingready.State {
	if reporter, ok := module.(ForwardingFailureReporter); ok {
		return reporter.ForwardingFailureImpact()
	}
	return forwardingready.State{IPv4: true, IPv6: true}
}
