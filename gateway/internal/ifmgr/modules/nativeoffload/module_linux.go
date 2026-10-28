//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/ti-mo/conntrack"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/offload"
)

const moduleName = "nativeoffload"

type connectionIdentity struct {
	id       uint32
	zone     uint16
	original conntrack.Tuple
	reply    conntrack.Tuple
}

type admittedFlow struct {
	flow        conntrack.Flow
	spec        offload.NativePairSpec
	guards      [2]offload.NativePacketGuard
	result      offload.NativePairResult
	uncertain   bool
	wan         string
	timeout     uint32
	counters    offload.NativeCounterHistory
	observation packetObservation
}

// AdmissionStats counts runtime decisions without packet-by-packet logs.
type AdmissionStats struct {
	Installed             uint64
	Retired               uint64
	Suspended             uint64
	Resumed               uint64
	Evicted               uint64
	Expired               uint64
	Fallback              uint64
	CounterFailures       uint64
	CounterPendingQueries uint64
	Active                uint32
	Tracked               uint32
	ResumeCandidates      uint32
	CleanupUncertain      uint32
	ObservationSkipped    uint64
	Paused                bool
	Failure               error
}

// Module keeps native ownership behind the daemon's forwarding mutation guards.
type Module struct {
	ifmgr.BaseModule
	cfg                  Config
	runtime              *offload.NativeRuntime
	startupFailed        bool
	ownershipUncertain   bool
	updater              *offload.ConntrackUpdater
	links                map[int]boundLink
	flows                map[connectionIdentity]*admittedFlow
	resumeCandidates     map[connectionIdentity]*admittedFlow
	observations         chan packetObservation
	failures             chan error
	events               *offload.ConntrackEvents
	forwarding           *offload.ForwardingEvents
	evidenceGeneration   uint64
	attempts             observationCache
	admissionPaused      atomic.Bool
	resumeWake           chan struct{}
	resumePass           *resumption
	cachedEvidence       *evidenceCheck
	captureMu            sync.Mutex
	captures             []*packetCapture
	capturesStopped      bool
	cancel               context.CancelFunc
	done                 chan struct{}
	unregisterForwarding func()
	unregisterNPT        func()
	policyGeneration     atomic.Uint64
	stats                AdmissionStats
	shutdownOnce         sync.Once
	shutdownErr          error
}

// New keeps native acceleration opt-in through the checked module configuration.
func New(configuration ifmgr.ModuleConfig) (ifmgr.Module, error) {
	var settings Config
	if configuration != nil {
		var ok bool
		settings, ok = configuration.(Config)
		if !ok {
			return nil, fmt.Errorf("nativeoffload: invalid configuration type %T", configuration)
		}
	}
	var module Module
	module.BaseModule = ifmgr.NewBaseModule(moduleName)
	module.cfg = settings
	return &module, nil
}

// Init requires kernel evidence and mutation guards before native ownership can start.
func (module *Module) Init(ctx context.Context, env *ifmgr.Env) (resultErr error) {
	module.InitBase(env, "module", moduleName)
	defer func() {
		if resultErr != nil && !errors.Is(resultErr, ifmgr.ErrModuleDisabled) {
			module.Log.WarnContext(ctx, "native admission initialization failed", "err", resultErr)
		}
	}()
	if !module.cfg.Enabled {
		return fmt.Errorf("%w: native offload is disabled", ifmgr.ErrModuleDisabled)
	}
	if env.LiveState == nil || env.ForwardingChanges == nil {
		return fmt.Errorf("native offload requires live policy and forwarding mutation guards")
	}
	if err := ValidateSupport(module.cfg); err != nil {
		return err
	}
	module.links = make(map[int]boundLink)
	module.flows = make(map[connectionIdentity]*admittedFlow)
	module.resumeCandidates = make(map[connectionIdentity]*admittedFlow)
	module.observations = make(chan packetObservation, int(module.cfg.Settings.ObservationCapacity))
	module.failures = make(chan error, 1)
	module.resumeWake = make(chan struct{}, 1)
	module.policyGeneration.Store(1)
	module.attempts = observationCache{entries: make(map[observationKey]*list.Element), order: list.List{}, capacity: int(module.cfg.Settings.FlowCapacity)}
	var err error
	module.unregisterForwarding, err = env.ForwardingChanges.Register(module.guardForwarding)
	if err != nil {
		return fmt.Errorf("register native forwarding guard: %w", err)
	}
	module.unregisterNPT, err = env.LiveState.RegisterNPTGuard(module.guardNPT)
	if err != nil {
		return fmt.Errorf("register native NPT guard: %w", err)
	}
	workerContext, cancel := context.WithCancel(ifmgr.WithoutForwardingChange(ctx))
	module.cancel = cancel
	module.forwarding, err = offload.NewForwardingEvents(workerContext, int(module.cfg.Settings.SocketBufferBytes))
	if err != nil {
		return fmt.Errorf("start native forwarding observations: %w", err)
	}
	module.events, err = offload.NewConntrackEvents(workerContext, int(module.cfg.Settings.EventCapacity), int(module.cfg.Settings.SocketBufferBytes))
	if err != nil {
		return fmt.Errorf("start native conntrack observations: %w", err)
	}
	module.updater, err = offload.NewConntrackUpdater(module.cfg.Settings.RequestTimeout)
	if err != nil {
		return fmt.Errorf("open native conntrack updater: %w", err)
	}
	module.done = make(chan struct{})
	go func() {
		defer close(module.done)
		defer module.stopCaptures()
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
				module.Log.ErrorContext(workerContext, "native admission loop panicked", "err", cause)
				module.recoverWorkerPanic(workerContext, "native admission loop", cause)
			}
		}()
		module.run(workerContext)
	}()
	return nil
}

func (module *Module) guardNPT(ctx context.Context, _ []string) (func(), error) {
	_, finish, err := module.Env.BeginForwardingChange(ctx)
	if err != nil {
		module.Log.WarnContext(ctx, "native NPT mutation preparation failed", "err", err)
		return nil, fmt.Errorf("prepare native NPT mutation: %w", err)
	}
	return finish, nil
}

// Reconcile creates native ownership after the preceding WAN modules apply their policy.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	module.Lock()
	defer module.Unlock()
	if module.stats.Failure != nil {
		return module.stats.Failure
	}
	if module.runtime != nil {
		if err := module.verifyLinks(); err != nil {
			return module.fail(ctx, err)
		}
		return nil
	}
	configuration, links, err := module.resolveGraph()
	if err != nil {
		return err
	}
	module.links = links
	for _, link := range links {
		if !link.hasOrigin {
			continue
		}
		captureContext := context.WithoutCancel(ifmgr.WithoutForwardingChange(ctx))
		capture, err := openPacketCapture(captureContext, &link.kernel, int(module.cfg.Settings.SocketBufferBytes), module.observations, module.failures, &module.admissionPaused)
		if err != nil {
			return module.fail(ctx, err)
		}
		if err := module.retainCapture(capture); err != nil {
			return module.fail(ctx, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return module.fail(ctx, fmt.Errorf("native startup canceled before ownership: %w", err))
	}
	module.runtime, err = offload.NewNativeRuntime(configuration, log)
	if err != nil {
		module.startupFailed = true
		return module.fail(ctx, fmt.Errorf("initialize native packet worker: %w", err))
	}
	log.InfoContext(ctx, "native automatic admission enabled", "pair_capacity", module.cfg.Settings.PairCapacity, "flow_capacity", module.cfg.Settings.FlowCapacity, "observation_capacity", module.cfg.Settings.ObservationCapacity)
	return nil
}

func (module *Module) guardForwarding(ctx context.Context) (func(), error) {
	module.Lock()
	locked := true
	defer func() {
		if locked {
			module.Unlock()
		}
	}()
	if module.startupFailed && module.runtime != nil {
		failure := module.stats.Failure
		module.Log.WarnContext(ctx, "native startup ownership blocks forwarding changes", "err", failure)
		return nil, fmt.Errorf("native startup retains resources: %w", failure)
	}
	if module.ownershipUncertain && module.runtime != nil {
		failure := module.stats.Failure
		module.Log.WarnContext(ctx, "uncertain native ownership blocks forwarding changes", "err", failure)
		return nil, fmt.Errorf("native failure retains uncertain resources: %w", failure)
	}
	if err := module.guardRuntime(ctx); err != nil {
		return nil, err
	}
	if err := module.suspendAll(ctx, "forwarding change"); err != nil {
		return nil, err
	}
	module.policyGeneration.Add(1)
	locked = false
	return module.Unlock, nil
}

// OnKernelEvent suspends rules before a changed kernel observation can permit admission.
func (module *Module) OnKernelEvent(ctx context.Context, _ *slog.Logger, _ netif.Event) error {
	module.Lock()
	defer module.Unlock()
	module.policyGeneration.Add(1)
	if err := module.suspendAll(ctx, "kernel forwarding observation changed"); err != nil {
		return err
	}
	module.requestResumeReconcile()
	return nil
}

// Stats keeps active ownership separate from retained and uncertain cleanup.
func (module *Module) Stats() AdmissionStats {
	module.Lock()
	defer module.Unlock()
	result := module.stats
	tracked, trackedErr := checkedNativeIndex(len(module.flows))
	resumable, resumableErr := checkedNativeIndex(len(module.resumeCandidates))
	if err := errors.Join(trackedErr, resumableErr); err != nil {
		module.Log.Error("native admission statistics exceed the ABI range", "err", err)
		result.Failure = errors.Join(result.Failure, err)
		return result
	}
	result.Tracked = tracked
	result.ResumeCandidates = resumable
	result.Active = 0
	result.CleanupUncertain = 0
	result.Paused = module.admissionPaused.Load()
	for _, entry := range module.flows {
		if entry.result.InstalledDirections != 0 {
			result.Active++
		}
		if entry.uncertain || entry.result.CleanupFailedDirections != 0 {
			result.CleanupUncertain++
		}
	}
	for _, capture := range module.captures {
		result.ObservationSkipped += capture.skipped.Load()
	}
	return result
}

func (module *Module) checkEvidence(ctx context.Context) bool {
	if err := errors.Join(module.events.Err(), module.forwarding.Err()); err != nil {
		_ = module.fail(ctx, err)
		return false
	}
	generation := module.forwarding.Generation()
	if generation != module.evidenceGeneration || module.cachedEvidence != nil && module.cachedEvidence.epoch != module.currentEvidenceEpoch() {
		module.invalidateEvidence(ctx)
	}
	return module.stats.Failure == nil
}

func (module *Module) fail(ctx context.Context, failure error) error {
	module.recordFailure(failure)
	return module.retireFailed(ctx)
}

func (module *Module) recordFailure(failure error) {
	module.admissionPaused.Store(true)
	module.resumePass = nil
	module.stats.Failure = errors.Join(module.stats.Failure, failure)
}

func (module *Module) retireFailed(ctx context.Context) error {
	module.stats.Failure = errors.Join(module.stats.Failure, module.retireAll(ctx, "native evidence became unavailable"))
	module.Log.ErrorContext(ctx, "native automatic admission stopped", "err", module.stats.Failure)
	return module.stats.Failure
}

// Shutdown retains the runtime until its final close confirms native resource cleanup.
func (module *Module) Shutdown(ctx context.Context, _ *slog.Logger) error {
	module.shutdownOnce.Do(func() {
		if module.cancel != nil {
			module.cancel()
		}
		if module.events != nil {
			module.shutdownErr = errors.Join(module.shutdownErr, module.events.Close())
		}
		if module.forwarding != nil {
			module.shutdownErr = errors.Join(module.shutdownErr, module.forwarding.Close())
		}
		for _, capture := range module.captureSnapshot() {
			module.shutdownErr = errors.Join(module.shutdownErr, capture.close())
			<-capture.done
		}
		if module.done != nil {
			<-module.done
		}
		if module.unregisterNPT != nil {
			module.unregisterNPT()
		}
		if module.unregisterForwarding != nil {
			module.unregisterForwarding()
		}
		module.Lock()
		defer module.Unlock()
		shutdownContext, cancel := context.WithTimeout(ctx, module.cfg.Settings.ShutdownTimeout)
		defer cancel()
		module.shutdownErr = errors.Join(module.shutdownErr, module.retireAll(shutdownContext, "shutdown"))
		if module.runtime != nil {
			closeErr := module.runtime.Close(shutdownContext)
			module.shutdownErr = errors.Join(module.shutdownErr, closeErr)
			if closeErr == nil {
				clear(module.flows)
				clear(module.resumeCandidates)
				module.ownershipUncertain = false
			}
		}
		if module.updater != nil {
			module.shutdownErr = errors.Join(module.shutdownErr, module.updater.Close())
		}
	})
	return module.shutdownErr
}

func flowIdentity(flow conntrack.Flow) connectionIdentity {
	return connectionIdentity{id: flow.ID, zone: flow.Zone, original: flow.TupleOrig, reply: flow.TupleReply}
}

func init() { ifmgr.Register(moduleName, New) }
