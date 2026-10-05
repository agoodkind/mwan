package netif

// StripPrefix returns the address half of a CIDR like "3d06:bad:b01:ff::1/128"
// or "10.0.0.1/24". If the input has no slash it is returned unchanged. The
// function is exported because both netif's own tests and downstream callers
// (notably the daemon main wiring that derives a source IP for policy rules
// from a CIDR config field) need it.
//
// This is the only string-level CIDR helper we need; everything else operates
// on parsed [netip.Addr], [netip.Prefix], or the netlink type system. Keeping it
// minimal and intentional.
func StripPrefix(cidr string) string {
	for i, c := range cidr {
		if c == '/' {
			return cidr[:i]
		}
	}
	return cidr
}
