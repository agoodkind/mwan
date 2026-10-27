//go:build linux && cgo && nativeoffload

package nativeoffload

import "goodkind.io/mwan/internal/forwardingready"

// A rejected startup without a native owner leaves Linux policy authoritative.
// The daemon still verifies its routing, protection and address dependencies.
func (module *Module) ForwardingFailureImpact() forwardingready.State {
	module.Lock()
	defer module.Unlock()
	failed := !module.startupFailed || module.runtime != nil
	return forwardingready.State{IPv4: failed, IPv6: failed}
}
