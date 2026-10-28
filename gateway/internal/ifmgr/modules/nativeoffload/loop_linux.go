//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"
)

func (module *Module) withLock(operation func()) {
	module.Lock()
	defer module.Unlock()
	operation()
}

func (module *Module) run(ctx context.Context) {
	counter := time.NewTicker(module.cfg.Settings.CounterInterval)
	defer counter.Stop()
	resync := time.NewTicker(module.cfg.Settings.ResyncInterval)
	defer resync.Stop()
	events, failures := module.events.Events, module.events.Errors
	changes, forwardingFailures := module.forwarding.Changes, module.forwarding.Errors
	for {
		select {
		case <-ctx.Done():
			return
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			module.withLock(func() { module.observeConnection(ctx, event) })
		case failure, open := <-failures:
			if !open {
				failures = nil
				continue
			}
			module.handleFailure(ctx, failure)
		case failure := <-module.failures:
			module.handleFailure(ctx, failure)
		case _, open := <-changes:
			if !open {
				changes = nil
				continue
			}
			module.withLock(func() { module.checkEvidence(ctx) })
		case failure, open := <-forwardingFailures:
			if !open {
				forwardingFailures = nil
				continue
			}
			module.handleFailure(ctx, failure)
		case observation := <-module.observations:
			module.withLock(func() {
				if module.readyForWork(ctx) {
					module.admit(ctx, observation)
				}
			})
		case <-module.resumeWake:
			module.withLock(func() { module.resumeOne(ctx) })
		case <-counter.C:
			module.maintain(ctx)
		case <-resync.C:
			module.resync(ctx)
		}
	}
}

func (module *Module) readyForWork(ctx context.Context) bool {
	return module.stats.Failure == nil && module.runtime != nil && module.checkEvidence(ctx)
}

func (module *Module) maintain(ctx context.Context) {
	sampled := false
	module.withLock(func() {
		if module.readyForWork(ctx) {
			module.sweep(ctx)
			sampled = true
		}
	})
	if sampled {
		module.logCounters(ctx)
	}
}

func (module *Module) resync(ctx context.Context) {
	module.withLock(func() {
		if module.stats.Failure == nil && module.runtime != nil {
			if err := module.verifyLinks(); err != nil {
				_ = module.fail(ctx, err)
			}
		}
	})
}

func (module *Module) stopCaptures() {
	module.captureMu.Lock()
	module.capturesStopped = true
	captures := slices.Clone(module.captures)
	module.captureMu.Unlock()
	for _, capture := range captures {
		if err := capture.close(); err != nil {
			module.Log.Warn("native packet observation close failed", "err", err)
		}
	}
}

func (module *Module) captureSnapshot() []*packetCapture {
	module.captureMu.Lock()
	defer module.captureMu.Unlock()
	return slices.Clone(module.captures)
}

func (module *Module) retainCapture(capture *packetCapture) error {
	module.captureMu.Lock()
	defer module.captureMu.Unlock()
	if module.capturesStopped {
		failure := errors.Join(fmt.Errorf("native packet observation owner already stopped"), capture.close())
		module.Log.Warn("native packet observation cannot retain its socket", "err", failure)
		return failure
	}
	module.captures = append(module.captures, capture)
	return nil
}
