package ifmgr

import "goodkind.io/mwan/internal/forwardingready"

// Modules without this contract invalidate both families.
type forwardingFailureReporter interface {
	ForwardingFailureImpact() forwardingready.State
}

func forwardingFailureImpact(module Module) forwardingready.State {
	if reporter, ok := module.(forwardingFailureReporter); ok {
		return reporter.ForwardingFailureImpact()
	}
	return forwardingready.State{IPv4: true, IPv6: true}
}
