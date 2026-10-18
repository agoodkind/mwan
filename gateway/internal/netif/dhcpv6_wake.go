package netif

// Wake never blocks the caller, allowing the links module event handler to call
// it through the addresses module. The client restarts the Solicit schedule at
// SOL_TIMEOUT only while it does not have a lease.
// The client ignores the signal while it has a lease.
func (client *DHCPv6PDClient) Wake() {
	select {
	case client.wake <- struct{}{}:
	default:
	}
}

// A fresh acquisition starts at SOL_TIMEOUT and does not need an earlier wake signal.
func (client *DHCPv6PDClient) solicitWake(lease DHCPv6PDLease) <-chan struct{} {
	if len(lease.Prefixes) != 0 || len(lease.Addresses) != 0 {
		return nil
	}
	select {
	case <-client.wake:
	default:
	}
	return client.wake
}
