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
	"fmt"
)

func (worker *NativeRuntime) executeVXLAN(owner *C.struct_mwan_native, request nativeRequest) nativeReply {
	var reply nativeReply
	var emptyContext NativeVXLANContext
	var result C.struct_mwan_native_vxlan_result
	var detail C.struct_mwan_native_error
	var rc C.int
	switch request.operation {
	case nativeVXLANOpen:
		rc = C.mwan_native_vxlan_context_open(owner, &request.vxlanEndpoint, &result, &detail)
	case nativeVXLANInstall:
		return worker.installVXLANOnOwner(owner, request)
	case nativeVXLANQuery:
		var stats C.struct_mwan_native_vxlan_stats
		rc = C.mwan_native_vxlan_query(owner, request.vxlanRule, &stats, &detail)
		result = stats.result
		reply.vxlanStats.Counter = decodeNativeCounter(stats.counter)
	case nativeVXLANRemove:
		rc = C.mwan_native_vxlan_remove(owner, request.vxlanRule, &result, &detail)
	case nativeVXLANClose:
		rc = C.mwan_native_vxlan_context_close(owner, request.handle, &result, &detail)
	case nativeEnable, nativeInstall, nativeQuery, nativeRemove, nativeDisable, nativeShutdown, nativeRetirement, nativeResume, nativeReobserve:
		reply.err = fmt.Errorf("native operation %d is not a VXLAN operation", request.operation)
		return reply
	default:
		reply.err = fmt.Errorf("unknown native VXLAN operation %d", request.operation)
		return reply
	}
	reply.vxlan = decodeNativeVXLANResult(result)
	reply.vxlanStats.Result = reply.vxlan
	reply.err = nativeCallError(rc, detail)
	if reply.err != nil && reply.vxlan.Context == emptyContext {
		reply.vxlan = worker.retainedVXLANResult(request)
		reply.vxlanStats.Result = reply.vxlan
	}
	worker.retainVXLANResult(reply.vxlan)
	return reply
}

func (worker *NativeRuntime) retainedVXLANResult(request nativeRequest) NativeVXLANResult {
	contextHandle := NativeVXLANContext{
		instance: uint64(request.handle.instance), generation: uint64(request.handle.generation), slot: uint32(request.handle.slot),
	}
	if request.operation == nativeVXLANQuery || request.operation == nativeVXLANRemove {
		requested := decodeNativeVXLANResult(request.vxlanRule)
		owned := worker.vxlan[requested.Context]
		if owned.result.RuleGeneration != requested.RuleGeneration {
			var absent NativeVXLANResult
			return absent
		}
		contextHandle = requested.Context
	}
	return worker.vxlan[contextHandle].result
}

func (worker *NativeRuntime) retainVXLANResult(result NativeVXLANResult) {
	var emptyContext NativeVXLANContext
	if result.Context == emptyContext {
		return
	}
	if result.State == NativeVXLANContextAbsent {
		delete(worker.vxlan, result.Context)
		return
	}
	owned := worker.vxlan[result.Context]
	owned.result = result
	if result.State == NativeVXLANContextReady {
		owned.evidence = nil
	}
	worker.vxlan[result.Context] = owned
}

func (worker *NativeRuntime) installVXLANOnOwner(owner *C.struct_mwan_native, request nativeRequest) nativeReply {
	var reply nativeReply
	var emptyContext NativeVXLANContext
	reply.vxlan = worker.retainedVXLANResult(request)
	if !request.deadline.IsZero() && worker.clock.Now().After(request.deadline) {
		reply.err = context.DeadlineExceeded
		return reply
	}
	if err := checkNativeEvidence(request.evidence); err != nil {
		reply.err = err
		return reply
	}
	var result C.struct_mwan_native_vxlan_result
	var detail C.struct_mwan_native_error
	rc := C.mwan_native_vxlan_install(owner, request.handle, &request.vxlanPlan, &result, &detail)
	if decoded := decodeNativeVXLANResult(result); decoded.Context != emptyContext {
		reply.vxlan = decoded
	}
	reply.err = nativeCallError(rc, detail)
	worker.retainVXLANResult(reply.vxlan)
	if rc != 0 || reply.vxlan.State != NativeVXLANRuleInstalled {
		return reply
	}
	worker.vxlan[reply.vxlan.Context] = nativeVXLANOwnership{result: reply.vxlan, evidence: request.evidence}
	if failure := checkNativeEvidence(request.evidence); failure != nil {
		removed, err := worker.removeVXLANOnOwner(owner, reply.vxlan)
		if removed.Context == reply.vxlan.Context {
			reply.vxlan = removed
		}
		reply.err = errors.Join(reply.err, failure, err)
	}
	return reply
}

func (worker *NativeRuntime) removeVXLANOnOwner(owner *C.struct_mwan_native, rule NativeVXLANResult) (NativeVXLANResult, error) {
	var result C.struct_mwan_native_vxlan_result
	var detail C.struct_mwan_native_error
	rc := C.mwan_native_vxlan_remove(owner, encodeNativeVXLANResult(rule), &result, &detail)
	decoded := decodeNativeVXLANResult(result)
	worker.retainVXLANResult(decoded)
	return decoded, nativeCallError(rc, detail)
}

func (worker *NativeRuntime) retireInvalidVXLANEvidence(owner *C.struct_mwan_native) error {
	var failures error
	for _, owned := range worker.vxlan {
		if owned.evidence == nil {
			continue
		}
		if failure := checkNativeEvidence(owned.evidence); failure != nil {
			if _, err := worker.removeVXLANOnOwner(owner, owned.result); err != nil {
				failures = errors.Join(failures, failure, err)
				continue
			}
			worker.log.Warn("native evidence invalidated VXLAN rule", "err", failure)
		}
	}
	return failures
}
