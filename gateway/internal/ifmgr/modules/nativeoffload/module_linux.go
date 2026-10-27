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
	"time"

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
	lastActive  time.Time
	lastSample  time.Time
	previous    [2]offload.NativeCounter
	samples     uint8
	observation packetObservation
}

// AdmissionStats counts runtime decisions without packet-by-packet logs.
type AdmissionStats struct {
	Installed          uint64
	Retired            uint64
	Suspended          uint64
	Resumed            uint64
	Evicted            uint64
	Expired            uint64
	Fallback           uint64
	CounterFailures    uint64
	Active             uint32
	Tracked            uint32
	ResumeCandidates   uint32
	CleanupUncertain   uint32
	ObservationSkipped uint64
	Paused             bool
	Failure            error
}

type Module struct {
	ifmgr.BaseModule
	cfg                  Config
	runtime              *offload.NativeRuntime
	startupFailed        bool
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
	captures             []*packetCapture
	cancel               context.CancelFunc
	workerContext        context.Context
	done                 chan struct{}
	unregisterForwarding func()
	unregisterNPT        func()
	policyGeneration     atomic.Uint64
	stats                AdmissionStats
	shutdownOnce         sync.Once
	shutdownErr          error
}

func New(configuration ifmgr.ModuleConfig) (ifmgr.Module, error) {
	var settings Config
	if configuration != nil {
		var ok bool
		settings, ok = configuration.(Config)
		if !ok {
			return nil, fmt.Errorf("nativeoffload: invalid configuration type %T", configuration)
		}
	}
	return &Module{BaseModule: ifmgr.NewBaseModule(moduleName), cfg: settings}, nil
}

func (module *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	module.InitBase(env, "module", moduleName)
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
	module.done = make(chan struct{})
	module.policyGeneration.Store(1)
	module.attempts = observationCache{entries: make(map[observationKey]*list.Element), capacity: int(module.cfg.Settings.FlowCapacity)}
	var err error
	module.unregisterForwarding, err = env.ForwardingChanges.Register(module.guardForwarding)
	if err != nil {
		return err
	}
	module.unregisterNPT, err = env.LiveState.RegisterNPTGuard(func(ctx context.Context, _ []string) (func(), error) {
		_, finish, err := env.BeginForwardingChange(ctx)
		return finish, err
	})
	if err != nil {
		return err
	}
	workerContext, cancel := context.WithCancel(ifmgr.WithoutForwardingChange(ctx))
	module.cancel = cancel
	module.workerContext = workerContext
	module.forwarding, err = offload.NewForwardingEvents(workerContext, int(module.cfg.Settings.SocketBufferBytes))
	if err != nil {
		return err
	}
	module.events, err = offload.NewConntrackEvents(workerContext, int(module.cfg.Settings.EventCapacity), int(module.cfg.Settings.SocketBufferBytes))
	if err != nil {
		return err
	}
	module.updater, err = offload.NewConntrackUpdater(module.cfg.Settings.RequestTimeout)
	if err != nil {
		return err
	}
	go module.run(workerContext)
	return nil
}

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
		capture, err := openPacketCapture(module.workerContext, &link.kernel, int(module.cfg.Settings.SocketBufferBytes), module.observations, module.failures, &module.admissionPaused)
		if err != nil {
			return module.fail(ctx, err)
		}
		module.captures = append(module.captures, capture)
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
	if module.startupFailed && module.runtime != nil {
		failure := module.stats.Failure
		module.Unlock()
		return nil, fmt.Errorf("native startup retains resources: %w", failure)
	}
	if err := module.suspendAll(ctx, "forwarding change"); err != nil {
		module.Unlock()
		return nil, err
	}
	module.policyGeneration.Add(1)
	return module.Unlock, nil
}

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

func (module *Module) Stats() AdmissionStats {
	module.Lock()
	defer module.Unlock()
	result := module.stats
	result.Tracked = uint32(len(module.flows))
	result.ResumeCandidates = uint32(len(module.resumeCandidates))
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

func (module *Module) run(ctx context.Context) {
	defer close(module.done)
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
			module.Lock()
			module.observeConnection(ctx, event)
			module.Unlock()
		case err, open := <-failures:
			if !open {
				failures = nil
				continue
			}
			module.Lock()
			_ = module.fail(ctx, err)
			module.Unlock()
		case err := <-module.failures:
			module.Lock()
			_ = module.fail(ctx, err)
			module.Unlock()
		case _, open := <-changes:
			if !open {
				changes = nil
				continue
			}
			module.Lock()
			module.checkEvidence(ctx)
			module.Unlock()
		case err, open := <-forwardingFailures:
			if !open {
				forwardingFailures = nil
				continue
			}
			module.Lock()
			_ = module.fail(ctx, err)
			module.Unlock()
		case observation := <-module.observations:
			module.Lock()
			if module.stats.Failure == nil && module.runtime != nil {
				if module.checkEvidence(ctx) {
					module.admit(ctx, observation)
				}
			}
			module.Unlock()
		case <-module.resumeWake:
			module.Lock()
			module.resumeOne(ctx)
			module.Unlock()
		case <-counter.C:
			sampled := false
			module.Lock()
			if module.stats.Failure == nil && module.runtime != nil {
				if module.checkEvidence(ctx) {
					module.sweep(ctx)
					sampled = true
				}
			}
			module.Unlock()
			if sampled {
				module.logCounters(ctx)
			}
		case <-resync.C:
			module.Lock()
			if module.stats.Failure == nil && module.runtime != nil {
				if err := module.verifyLinks(); err != nil {
					_ = module.fail(ctx, err)
				}
			}
			module.Unlock()
		}
	}
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
	module.admissionPaused.Store(true)
	module.resumePass = nil
	module.stats.Failure = errors.Join(module.stats.Failure, failure, module.retireAll(ctx, "native evidence became unavailable"))
	module.Log.ErrorContext(ctx, "native automatic admission stopped", "err", module.stats.Failure)
	return module.stats.Failure
}

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
		for _, capture := range module.captures {
			module.shutdownErr = errors.Join(module.shutdownErr, capture.close())
			<-capture.done
		}
		if module.done != nil && module.updater != nil {
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
