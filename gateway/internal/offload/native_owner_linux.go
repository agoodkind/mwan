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
	"syscall"
	"time"
)

func (worker *NativeRuntime) run(config C.struct_mwan_native_config, ready chan<- nativeReply) {
	var owner *C.struct_mwan_native
	var detail C.struct_mwan_native_error
	rc := C.mwan_native_open(&config, &owner, &detail)
	var initial nativeReply
	initial.err, initial.ownerExists = nativeCallError(rc, detail), owner != nil
	ready <- initial
	if owner == nil {
		return
	}
	var stats C.struct_mwan_native_worker_stats
	active := rc == 0
	for {
		var request nativeRequest
		if !active {
			request = <-worker.requests
		} else {
			select {
			case request = <-worker.requests:
			default:
				received, err := worker.poll(owner, &stats, &detail)
				if err != nil {
					worker.log.Error("native packet worker stopped admitting packets", "err", err)
					active = false
					continue
				}
				if !received && worker.idleWait > 0 {
					timer := time.NewTimer(worker.idleWait)
					select {
					case request = <-worker.requests:
						timer.Stop()
					case <-timer.C:
						continue
					}
				} else {
					continue
				}
			}
		}
		response, shutdown := worker.execute(&owner, request, &active)
		request.reply <- response
		if shutdown {
			return
		}
		if active {
			if _, err := worker.poll(owner, &stats, &detail); err != nil {
				worker.log.Error("native packet worker stopped admitting packets", "err", err)
				active = false
			}
		}
	}
}

func (worker *NativeRuntime) execute(owner **C.struct_mwan_native, request nativeRequest, active *bool) (nativeReply, bool) {
	var result nativeReply
	if worker.shuttingDown && request.operation != nativeShutdown {
		result.err = worker.closedError()
		return result, false
	}
	var detail C.struct_mwan_native_error
	var pair C.struct_mwan_native_pair_result
	var stats C.struct_mwan_native_pair_stats
	var rc C.int
	switch request.operation {
	case nativeEnable:
		rc = C.mwan_native_open(&request.config, owner, &detail)
		if rc == 0 {
			*active = true
		}
	case nativeInstall, nativeResume, nativeReobserve:
		result = worker.installOnOwner(*owner, request)
	case nativeRetirement:
		var retirement C.struct_mwan_native_retirement_stats
		rc = C.mwan_native_retirement_query(*owner, &retirement, &detail)
		result.retirement = NativeRetirementStats{
			ActivePairCapacity: uint32(retirement.active_pair_capacity), RetainedPairCapacity: uint32(retirement.retained_pair_capacity),
			RetainedPairs: uint32(retirement.retained_pairs), DrainingPairs: uint32(retirement.draining_pairs),
			RetainedDirections: uint64(retirement.retained_directions),
		}
	case nativeQuery:
		rc = C.mwan_native_pair_query(*owner, request.handle, &stats, &detail)
		result.stats = decodeNativePairStats(stats)
	case nativeRemove:
		rc = C.mwan_native_pair_remove(*owner, request.handle, &pair, &detail)
		result.pair = decodeNativePairResult(pair)
	case nativeVXLANOpen, nativeVXLANInstall, nativeVXLANQuery, nativeVXLANRemove, nativeVXLANClose:
		result = worker.executeVXLAN(*owner, request)
	case nativeDisable:
		rc = C.mwan_native_close(owner, C.MWAN_NATIVE_DISABLE, &detail)
		if rc == 0 {
			*active = false
		}
	case nativeShutdown:
		rc = C.mwan_native_close(owner, C.MWAN_NATIVE_FINAL_SHUTDOWN, &detail)
	}
	if result.err == nil {
		result.err = nativeCallError(rc, detail)
	}
	worker.completeEvidence(request.operation, result)
	result.ownerExists = *owner != nil
	if request.operation == nativeShutdown {
		worker.shuttingDown = true
		worker.mu.Lock()
		worker.terminalError = result.err
		worker.mu.Unlock()
		worker.closeUpdater()
		result.err = worker.shutdownError()
	}
	// Admission logs rejected counter evidence with its connection identity.
	if result.err != nil && request.operation != nativeQuery && (!request.observed || !nativeAdmissionFallback(result.err)) {
		worker.log.Warn("native operation failed", "operation", request.operation, "err", result.err)
	}
	return result, request.operation == nativeShutdown && rc == 0
}

func (worker *NativeRuntime) installOnOwner(owner *C.struct_mwan_native, request nativeRequest) nativeReply {
	var result nativeReply
	if !request.deadline.IsZero() && worker.clock.Now().After(request.deadline) {
		result.err = context.DeadlineExceeded
		return result
	}
	if err := checkNativeEvidence(request.evidence); err != nil {
		result.err = err
		return result
	}
	var pair C.struct_mwan_native_pair_result
	var detail C.struct_mwan_native_error
	var rc C.int
	switch {
	case request.operation == nativeReobserve:
		rc = C.mwan_native_pair_reobserve(owner, request.handle, &request.pair, &request.guards[0], &pair, &detail)
	case request.operation == nativeResume:
		rc = C.mwan_native_pair_resume_observed(owner, request.handle, &request.pair, &request.guards[0], &pair, &detail)
	case request.observed:
		rc = C.mwan_native_pair_install_observed(owner, &request.pair, &request.guards[0], &pair, &detail)
	default:
		rc = C.mwan_native_pair_install(owner, &request.pair, &pair, &detail)
	}
	result.pair = decodeNativePairResult(pair)
	result.err = nativeCallError(rc, detail)
	worker.revalidateInstalledEvidence(owner, request.evidence, &result)
	return result
}

func (worker *NativeRuntime) revalidateInstalledEvidence(owner *C.struct_mwan_native, evidence *NativeEvidenceGuard, result *nativeReply) {
	if result.pair.InstalledDirections == 0 || evidence == nil {
		return
	}
	worker.registerEvidence(result.pair.Handle, evidence)
	failure := checkNativeEvidence(evidence)
	if failure == nil {
		return
	}
	installed := result.pair
	var removed C.struct_mwan_native_pair_result
	var detail C.struct_mwan_native_error
	rc := C.mwan_native_pair_remove(owner, encodeNativeHandle(installed.Handle), &removed, &detail)
	if decoded := decodeNativePairResult(removed); decoded.Handle == installed.Handle {
		result.pair = decoded
	}
	result.err = errors.Join(result.err, failure, nativeCallError(rc, detail))
	if rc == 0 {
		worker.unregisterEvidence(installed.Handle)
	}
}

func (worker *NativeRuntime) completeEvidence(operation nativeOperation, result nativeReply) {
	if result.err != nil {
		return
	}
	switch operation {
	case nativeEnable, nativeInstall, nativeQuery, nativeRetirement, nativeResume, nativeReobserve,
		nativeVXLANOpen, nativeVXLANInstall, nativeVXLANQuery, nativeVXLANRemove, nativeVXLANClose:
		return
	case nativeRemove:
		worker.unregisterEvidence(result.pair.Handle)
	case nativeDisable, nativeShutdown:
		clear(worker.evidence)
		clear(worker.pairEvidence)
		clear(worker.vxlan)
	}
}

func nativeAdmissionFallback(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EEXIST) || errors.Is(err, syscall.EINVAL)
}
