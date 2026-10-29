//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"errors"
	"fmt"
)

type nativePanicError struct {
	cause error
}

func (failure *nativePanicError) Error() string { return failure.cause.Error() }

func (failure *nativePanicError) Unwrap() error { return failure.cause }

func panicFailure(operation string, cause error) *nativePanicError {
	return &nativePanicError{cause: fmt.Errorf("%s panicked: %w", operation, cause)}
}

func (module *Module) handleFailure(ctx context.Context, failure error) {
	var panicked *nativePanicError
	if errors.As(failure, &panicked) {
		module.stopAfterPanic(ctx, failure)
		return
	}
	module.withLock(func() { _ = module.fail(ctx, failure) })
}

func (module *Module) guardRuntime(ctx context.Context) error {
	if module.runtime == nil {
		return nil
	}
	if _, err := module.runtime.Stats(); err != nil {
		module.ownershipUncertain = true
		module.Log.WarnContext(ctx, "native runtime failure blocks forwarding changes", "err", err)
		return module.fail(ctx, fmt.Errorf("verify native runtime before forwarding mutation: %w", err))
	}
	return nil
}

func (module *Module) recoverWorkerPanic(ctx context.Context, operation string, cause error) {
	module.stopAfterPanic(ctx, panicFailure(operation, cause))
}

func (module *Module) stopAfterPanic(ctx context.Context, failure error) {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), module.cfg.Settings.ShutdownTimeout)
	defer cancel()
	module.Lock()
	defer module.Unlock()
	module.ownershipUncertain = module.runtime != nil
	defer func() {
		if recovered := recover(); recovered != nil {
			var cause error
			switch value := recovered.(type) {
			case error:
				cause = value
			case string:
				cause = errors.New("panic value: " + value)
			default:
				cause = fmt.Errorf("panic value: %v", value)
			}
			cleanupFailure := panicFailure("native panic cleanup", cause)
			module.admissionPaused.Store(true)
			module.resumePass = nil
			module.stats.Failure = errors.Join(module.stats.Failure, cleanupFailure)
			module.Log.ErrorContext(cleanupContext, "native panic cleanup retained ownership", "err", cleanupFailure)
		}
	}()
	module.recordFailure(failure)
	module.stopCaptures()
	if module.cancel != nil {
		module.cancel()
	}
	_ = module.retireFailed(cleanupContext)
}
