//go:build linux && cgo && nativeoffload

package nativeoffload

import "goodkind.io/mwan/internal/forwardingready"

// ForwardingFailureImpact preserves Linux readiness after startup rejects an owner-free runtime.
// The daemon still verifies its routing, protection and address dependencies.
func (module *Module) ForwardingFailureImpact() forwardingready.State {
	module.Lock()
	defer module.Unlock()
	failed := !module.startupFailed || module.runtime != nil
	return forwardingready.State{IPv4: failed, IPv6: failed}
}
