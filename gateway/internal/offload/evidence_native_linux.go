//go:build linux && cgo && nativeoffload

package offload

/*
#cgo pkg-config: mwan-native
#include <mwan_native.h>
*/
import "C"

import (
	"errors"
	"fmt"
)

func checkNativeEvidence(evidence *NativeEvidenceGuard) error {
	if evidence == nil {
		return nil
	}
	if evidence.Check == nil {
		return fmt.Errorf("native evidence guard requires a check")
	}
	return evidence.Check()
}

func (worker *NativeRuntime) unregisterEvidence(handle NativePairHandle) {
	evidence, registered := worker.pairEvidence[handle]
	if !registered {
		return
	}
	delete(worker.pairEvidence, handle)
	delete(worker.evidence[evidence], handle)
	if len(worker.evidence[evidence]) == 0 {
		delete(worker.evidence, evidence)
	}
}

func (worker *NativeRuntime) registerEvidence(handle NativePairHandle, evidence *NativeEvidenceGuard) {
	worker.unregisterEvidence(handle)
	if worker.evidence[evidence] == nil {
		worker.evidence[evidence] = make(map[NativePairHandle]struct{})
	}
	worker.evidence[evidence][handle] = struct{}{}
	worker.pairEvidence[handle] = evidence
}

func (worker *NativeRuntime) retireInvalidEvidence(owner *C.struct_mwan_native) error {
	var failures error
	for evidence, handles := range worker.evidence {
		if failure := checkNativeEvidence(evidence); failure != nil {
			var cleanupError error
			for handle := range handles {
				var result C.struct_mwan_native_pair_result
				var detail C.struct_mwan_native_error
				rc := C.mwan_native_pair_remove(owner, encodeNativeHandle(handle), &result, &detail)
				if err := nativeCallError(rc, detail); err != nil {
					cleanupError = errors.Join(cleanupError, err)
					continue
				}
				worker.unregisterEvidence(handle)
			}
			if cleanupError != nil {
				failures = errors.Join(failures, failure, cleanupError)
				continue
			}
			worker.log.Warn("native evidence invalidated installed rules", "err", failure)
		}
	}
	return failures
}

func (worker *NativeRuntime) poll(owner *C.struct_mwan_native, stats *C.struct_mwan_native_worker_stats, detail *C.struct_mwan_native_error) (bool, error) {
	if err := errors.Join(worker.retireInvalidEvidence(owner), worker.retireInvalidVXLANEvidence(owner)); err != nil {
		worker.mu.Lock()
		worker.pollError = err
		worker.mu.Unlock()
		return false, err
	}
	var emptyStats C.struct_mwan_native_worker_stats
	var emptyDetail C.struct_mwan_native_error
	*stats = emptyStats
	*detail = emptyDetail
	rc := C.mwan_native_poll(owner, C.uint32_t(worker.pollBudget), stats, detail)
	var err error
	if rc != 0 {
		err = nativeCallError(rc, *detail)
	}
	worker.mu.Lock()
	previous := worker.stats.Received
	worker.stats = decodeNativeWorkerStats(*stats)
	worker.pollError = err
	worker.mu.Unlock()
	return uint64(stats.received) != previous, err
}
