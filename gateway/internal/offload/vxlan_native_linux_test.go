//go:build linux && cgo && nativeoffload

package offload_test

import (
	"errors"
	"net/netip"
	"syscall"
	"testing"

	"github.com/ti-mo/conntrack"

	"goodkind.io/mwan/internal/offload"
)

func nativeVXLANPlan() offload.NativeVXLANPlan {
	var inner conntrack.Tuple
	inner.IP.SourceAddress = netip.MustParseAddr("2001:db8:1::1")
	inner.IP.DestinationAddress = netip.MustParseAddr("2001:db8:2::2")
	inner.Proto.Protocol = syscall.IPPROTO_TCP
	inner.Proto.SourcePort, inner.Proto.DestinationPort = 45001, 5227
	return offload.NativeVXLANPlan{
		Cookie: 1, PolicyGeneration: 1, Inner: inner,
		InnerSourceMAC: [6]byte{2, 0, 0, 0, 0, 1}, InnerDestinationMAC: [6]byte{2, 0, 0, 0, 0, 2},
		OuterSourceMAC: [6]byte{2, 0, 0, 0, 0, 1}, OuterDestinationMAC: [6]byte{2, 0, 0, 0, 0, 3},
		OuterSource: netip.MustParseAddrPort("192.0.2.1:49152"), OuterDestination: netip.MustParseAddrPort("198.51.100.2:4789"),
		VNI: 7702, InnerMTU: 1450, UnderlayMTU: 1500, OuterTTL: 64,
	}
}

func TestNativeVXLANPlanValidation(t *testing.T) {
	plan := nativeVXLANPlan()
	if err := offload.ValidateNativeVXLANPlan(plan); err != nil {
		t.Fatal(err)
	}
	plan.InnerMTU, plan.UnderlayMTU = 1280, 1330
	if err := offload.ValidateNativeVXLANPlan(plan); err != nil {
		t.Fatalf("minimum IPv6 MTU with the VXLAN envelope: %v", err)
	}
	plan.UnderlayMTU--
	requireNativeVXLANValidationError(t, offload.ValidateNativeVXLANPlan(plan))
}

func TestNativeVXLANPlanRejectsUnsupportedCapabilities(t *testing.T) {
	cases := []struct {
		name   string
		change func(*offload.NativeVXLANPlan)
	}{
		{"udp-inner", func(plan *offload.NativeVXLANPlan) { plan.Inner.Proto.Protocol = syscall.IPPROTO_UDP }},
		{"ipv4-inner", func(plan *offload.NativeVXLANPlan) {
			plan.Inner.IP.SourceAddress = netip.MustParseAddr("192.0.2.3")
			plan.Inner.IP.DestinationAddress = netip.MustParseAddr("198.51.100.4")
		}},
		{"zero-cookie", func(plan *offload.NativeVXLANPlan) { plan.Cookie = 0 }},
		{"zero-generation", func(plan *offload.NativeVXLANPlan) { plan.PolicyGeneration = 0 }},
		{"wildcard-source-port", func(plan *offload.NativeVXLANPlan) { plan.Inner.Proto.SourcePort = 0 }},
		{"multicast-inner-mac", func(plan *offload.NativeVXLANPlan) { plan.InnerDestinationMAC[0] = 1 }},
		{"multicast-outer-mac", func(plan *offload.NativeVXLANPlan) { plan.OuterDestinationMAC[0] = 1 }},
		{"outer-destination-port", func(plan *offload.NativeVXLANPlan) {
			plan.OuterDestination = netip.MustParseAddrPort("198.51.100.2:8472")
		}},
		{"outer-source-port", func(plan *offload.NativeVXLANPlan) { plan.OuterSource = netip.MustParseAddrPort("192.0.2.1:0") }},
		{"outer-multicast", func(plan *offload.NativeVXLANPlan) { plan.OuterDestination = netip.MustParseAddrPort("224.0.0.1:4789") }},
		{"vni-overflow", func(plan *offload.NativeVXLANPlan) { plan.VNI = 0x01000000 }},
		{"zero-ttl", func(plan *offload.NativeVXLANPlan) { plan.OuterTTL = 0 }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			plan := nativeVXLANPlan()
			test.change(&plan)
			requireNativeVXLANValidationError(t, offload.ValidateNativeVXLANPlan(plan))
		})
	}
}

func requireNativeVXLANValidationError(t *testing.T, err error) {
	t.Helper()
	var detail *offload.NativeError
	if !errors.Is(err, syscall.EINVAL) || !errors.As(err, &detail) || detail.Operation == 0 || detail.Message == "" {
		t.Fatalf("Rust validation did not return a structured invalid-plan error: %v", err)
	}
}
