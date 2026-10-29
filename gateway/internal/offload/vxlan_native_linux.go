//go:build linux && cgo && nativeoffload

package offload

/*
#cgo pkg-config: mwan-native
#include <mwan_native.h>
*/
import "C"

import (
	"context"
	"errors"
	"net/netip"

	"github.com/ti-mo/conntrack"
)

// NativeVXLANEndpoint identifies a declared kernel VF by PCI function and Linux interface index.
type NativeVXLANEndpoint struct {
	Receiver     NativeReceiverID
	LinuxIfIndex uint32
	SourceVF     uint16
	PCI          NativePCIAddress
}

// NativeVXLANContext prevents accidental use of plain-flow pair handles.
type NativeVXLANContext NativePairHandle

// NativeVXLANState distinguishes ready contexts, installed rules and uncertain cleanup.
type NativeVXLANState uint32

const (
	// NativeVXLANContextAbsent permits opening another context for the receiver.
	NativeVXLANContextAbsent NativeVXLANState = C.MWAN_NATIVE_VXLAN_CONTEXT_ABSENT
	// NativeVXLANContextReady permits an exact outbound rule installation.
	NativeVXLANContextReady NativeVXLANState = C.MWAN_NATIVE_VXLAN_CONTEXT_READY
	// NativeVXLANRuleInstalled reports ownership of a hardware encapsulation rule.
	NativeVXLANRuleInstalled NativeVXLANState = C.MWAN_NATIVE_VXLAN_RULE_INSTALLED
	// NativeVXLANCleanupFailed blocks reuse after uncertain hardware removal.
	NativeVXLANCleanupFailed NativeVXLANState = C.MWAN_NATIVE_VXLAN_CLEANUP_FAILED
)

// NativeVXLANPlan preserves the inner tuple. Go supplies the selected endpoints,
// Ethernet addresses, MTU budgets and current policy generation.
type NativeVXLANPlan struct {
	Cookie              uint64
	PolicyGeneration    uint64
	Inner               conntrack.Tuple
	InnerSourceMAC      [6]byte
	InnerDestinationMAC [6]byte
	OuterSourceMAC      [6]byte
	OuterDestinationMAC [6]byte
	OuterSource         netip.AddrPort
	OuterDestination    netip.AddrPort
	VNI                 uint32
	InnerMTU            uint16
	UnderlayMTU         uint16
	OuterTTL            uint8
	OuterTOS            uint8
}

// NativeVXLANResult retains context and rule identity after partial failures.
type NativeVXLANResult struct {
	Context        NativeVXLANContext
	RuleGeneration uint64
	State          NativeVXLANState
}

// NativeVXLANStats distinguishes unavailable counter evidence from a valid zero count.
type NativeVXLANStats struct {
	Result  NativeVXLANResult
	Counter NativeCounter
}

type nativeVXLANOwnership struct {
	result   NativeVXLANResult
	evidence *NativeEvidenceGuard
}

// ValidateNativeVXLANPlan calls the Rust validator without initializing EAL.
func ValidateNativeVXLANPlan(plan NativeVXLANPlan) error {
	_, err := encodeNativeVXLANPlan(plan)
	return err
}

// OpenVXLANContext uses the existing owner's declared kernel VF TX endpoint.
// Each receiver permits one context and one exact outbound IPv6 TCP rule.
func (worker *NativeRuntime) OpenVXLANContext(ctx context.Context, endpoint NativeVXLANEndpoint) (NativeVXLANResult, error) {
	var request nativeRequest
	request.operation = nativeVXLANOpen
	request.vxlanEndpoint.version = C.MWAN_NATIVE_VXLAN_VERSION
	request.vxlanEndpoint.record_size = C.sizeof_struct_mwan_native_vxlan_endpoint
	request.vxlanEndpoint.receiver_id = C.uint32_t(endpoint.Receiver)
	request.vxlanEndpoint.linux_ifindex = C.uint32_t(endpoint.LinuxIfIndex)
	request.vxlanEndpoint.source_vf = C.uint16_t(endpoint.SourceVF)
	request.vxlanEndpoint.pci = encodeNativePCI(endpoint.PCI)
	result := worker.call(ctx, request)
	return result.vxlan, result.err
}

// InstallVXLAN checks evidence before and after creation and between poll batches.
// Errors do not release the caller's context or a previously installed rule.
func (worker *NativeRuntime) InstallVXLAN(ctx context.Context, handle NativeVXLANContext, plan NativeVXLANPlan, evidence *NativeEvidenceGuard) (NativeVXLANResult, error) {
	if evidence == nil || evidence.Check == nil {
		return NativeVXLANResult{}, errors.New("VXLAN installation requires a forwarding evidence guard")
	}
	encoded, err := encodeNativeVXLANPlan(plan)
	if err != nil {
		return NativeVXLANResult{}, err
	}
	var request nativeRequest
	request.operation = nativeVXLANInstall
	request.handle = encodeNativeHandle(NativePairHandle(handle))
	request.vxlanPlan = encoded
	request.evidence = evidence
	request.deadline, _ = ctx.Deadline()
	result := worker.call(ctx, request)
	return result.vxlan, result.err
}

// QueryVXLAN preserves counter validity and errno when hardware sampling fails.
func (worker *NativeRuntime) QueryVXLAN(ctx context.Context, rule NativeVXLANResult) (NativeVXLANStats, error) {
	var request nativeRequest
	request.operation, request.vxlanRule = nativeVXLANQuery, encodeNativeVXLANResult(rule)
	result := worker.call(ctx, request)
	return result.vxlanStats, result.err
}

// RemoveVXLAN retains its context. Failed removal retains uncertain ownership.
// Successful removal does not prove Linux VXLAN fallback or TCP continuation.
func (worker *NativeRuntime) RemoveVXLAN(ctx context.Context, rule NativeVXLANResult) (NativeVXLANResult, error) {
	var request nativeRequest
	request.operation, request.vxlanRule = nativeVXLANRemove, encodeNativeVXLANResult(rule)
	result := worker.call(ctx, request)
	return result.vxlan, result.err
}

// CloseVXLANContext releases the context only after Rust confirms rule removal.
// Disable and final Close also remove VXLAN rules before stopping owned ports.
func (worker *NativeRuntime) CloseVXLANContext(ctx context.Context, handle NativeVXLANContext) (NativeVXLANResult, error) {
	var request nativeRequest
	request.operation = nativeVXLANClose
	request.handle = encodeNativeHandle(NativePairHandle(handle))
	result := worker.call(ctx, request)
	return result.vxlan, result.err
}

func encodeNativeVXLANPlan(plan NativeVXLANPlan) (C.struct_mwan_native_vxlan_plan, error) {
	var result C.struct_mwan_native_vxlan_plan
	inner, err := encodeNativeTuple(plan.Inner)
	if err != nil {
		return result, err
	}
	if !plan.OuterSource.Addr().Is4() || !plan.OuterDestination.Addr().Is4() {
		return result, errors.New("VXLAN outer endpoints require IPv4 addresses")
	}
	result.version, result.record_size = C.MWAN_NATIVE_VXLAN_VERSION, C.sizeof_struct_mwan_native_vxlan_plan
	result.cookie, result.policy_generation = C.uint64_t(plan.Cookie), C.uint64_t(plan.PolicyGeneration)
	result.inner = inner
	for index := range plan.InnerSourceMAC {
		result.inner_source_mac[index] = C.uint8_t(plan.InnerSourceMAC[index])
		result.inner_destination_mac[index] = C.uint8_t(plan.InnerDestinationMAC[index])
		result.outer_source_mac[index] = C.uint8_t(plan.OuterSourceMAC[index])
		result.outer_destination_mac[index] = C.uint8_t(plan.OuterDestinationMAC[index])
	}
	source, destination := plan.OuterSource.Addr().As4(), plan.OuterDestination.Addr().As4()
	for index := range source {
		result.outer_source[index], result.outer_destination[index] = C.uint8_t(source[index]), C.uint8_t(destination[index])
	}
	result.vni = C.uint32_t(plan.VNI)
	result.outer_source_port, result.outer_destination_port = C.uint16_t(plan.OuterSource.Port()), C.uint16_t(plan.OuterDestination.Port())
	result.inner_mtu, result.underlay_mtu = C.uint16_t(plan.InnerMTU), C.uint16_t(plan.UnderlayMTU)
	result.outer_ttl, result.outer_tos = C.uint8_t(plan.OuterTTL), C.uint8_t(plan.OuterTOS)
	var detail C.struct_mwan_native_error
	rc := C.mwan_native_vxlan_plan_validate(&result, &detail)
	return result, nativeCallError(rc, detail)
}

func encodeNativeVXLANResult(result NativeVXLANResult) C.struct_mwan_native_vxlan_result {
	var encoded C.struct_mwan_native_vxlan_result
	encoded.context = encodeNativeHandle(NativePairHandle(result.Context))
	encoded.rule_generation, encoded.state = C.uint64_t(result.RuleGeneration), C.uint32_t(result.State)
	return encoded
}

func decodeNativeVXLANResult(result C.struct_mwan_native_vxlan_result) NativeVXLANResult {
	return NativeVXLANResult{
		Context:        NativeVXLANContext{instance: uint64(result.context.instance), generation: uint64(result.context.generation), slot: uint32(result.context.slot)},
		RuleGeneration: uint64(result.rule_generation), State: NativeVXLANState(result.state),
	}
}
