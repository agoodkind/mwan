//go:build linux

package bpf

import "goodkind.io/mwan/internal/nptpolicy"

const (
	// HairpinMask selects the packet mark bits reserved for translated hairpin traffic.
	HairpinMask uint32 = 0xffff0000
	// HairpinTag identifies translated hairpin traffic in the reserved mark bits.
	HairpinTag uint32 = 0x4e500000
	// MaxPairs limits prefix pairs in one interface policy.
	MaxPairs = 64
)

// InterfacePolicy configures translation for one Linux interface.
type InterfacePolicy struct {
	IfIndex  int
	Internal bool
	Pairs    []nptpolicy.PrefixPair
}

// Direction selects the ingress or egress traffic-control hook.
type Direction string

const (
	// Ingress translates packets entering an interface.
	Ingress Direction = "ingress"
	// Egress translates packets leaving an interface.
	Egress Direction = "egress"
)

// AttachmentState reports the result for one interface and direction after Reconcile.
type AttachmentState struct {
	IfIndex   int
	Direction Direction
	Ready     bool
	Reason    string
}
