package npt

import "goodkind.io/mwan/internal/forwardingready"

// ForwardingFailureImpact excludes IPv6 after failed NPT reconciliation.
// IPv4 translation readiness is independently verified against current rules.
func (*Module) ForwardingFailureImpact() forwardingready.State {
	return forwardingready.State{IPv4: false, IPv6: true}
}
