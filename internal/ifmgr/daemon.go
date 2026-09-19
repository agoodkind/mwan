package ifmgr

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	internalclock "goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/tracing"
	"goodkind.io/mwan/internal/wanstate"
)

// Daemon is the long-lived ifmgr process. One Daemon serves one role on
// one interface. Multiple roles on multiple interfaces would be multiple
// Daemons (or, future work, a multi-iface Daemon).
//
// Lifecycle:
//  1. NewDaemon constructs the Daemon and instantiates each role module
//     via the registry. Init is called in dependency order.
//  2. Run starts the netif Monitor (and DHCP/RA clients if requested),
//     runs an initial Reconcile pass, then enters the main loop.
//  3. Main loop selects on ctx.Done, monitor events, DHCP lease events,
//     and a periodic ticker. Each tick: Reconcile every module then
//     EvaluateAlerts every module.
//  4. Run returns when ctx is cancelled. Modules' kernel state is left
//     in place (no Cleanup hook); the daemon is restart-safe.
type Daemon struct {
	cfg     DaemonConfig
	log     *slog.Logger
	role    string
	modules []Module
	clock   internalclock.Clock
	env     *Env

	// reconcileReq lets a module ask for an immediate reconcile pass instead
	// of waiting for the periodic tick. It is buffered with capacity 1 so a
	// burst of requests coalesces into a single pass; senders drop their
	// request when one is already queued. This is what makes health-driven
	// failover event-driven rather than tick-latency-bound.
	reconcileReq chan struct{}

	mu        sync.Mutex
	startedAt time.Time
}

// DaemonConfig captures the subset of cfg.IfMgr that the daemon needs.
// It is built by main.go from the parsed TOML, after the explicit config
// schema has been adapted into typed runtime module configs.
type DaemonConfig struct {
	Role              string
	Iface             string
	ReconcileInterval time.Duration

	// EnableDHCP causes the daemon to start a DHCPv4 client on Iface.
	// The client emits LeaseInfo events the daemon fans out to all
	// modules via OnDHCPLease.
	EnableDHCP  bool
	DHCPInitial time.Duration
	DHCPMax     time.Duration

	// EnableRA causes the daemon to open a Router Solicitation client
	// (mdlayher/ndp) and pass it to modules via env.RA. Without this,
	// modules that want to send RS get env.RA == nil and must operate
	// in passive (RA-monitoring-only) mode.
	EnableRA bool

	// Notifier is the boundary every email exits through. The daemon wires
	// it into env.Alerts via WrapNotifier so existing module call sites
	// keep using the AlertManager surface. cmd/mwan builds the Notifier
	// from cfg.Email plus cfg.Notify (via notify.FromConfig) so the
	// per-(kind, key) state machine and the email sink share one path.
	// A nil Notifier degrades to NullNotifier (journald-only via the
	// daemon's own logger; no email).
	Notifier notify.Notifier

	// ModuleConfigs holds per-module runtime configs keyed by module name.
	// Each module's Constructor receives ModuleConfigs[Name()].
	ModuleConfigs ModuleConfigSet

	// LiveState, when non-nil, receives each module's reconciled snapshot
	// for the management surface to serve. Hosts without a surface leave
	// it nil and modules skip the write.
	LiveState *wanstate.Store
}

// NewDaemon constructs a Daemon for the given config and role. Resolves
// the role to a list of module names, looks up each constructor, and
// instantiates the modules. Returns an error if any module is missing
// from the registry or any constructor fails.
func NewDaemon(log *slog.Logger, cfg DaemonConfig) (*Daemon, error) {
	if log == nil {
		return nil, fmt.Errorf("ifmgr.NewDaemon: log is required")
	}
	if cfg.Role == "" {
		return nil, fmt.Errorf("ifmgr.NewDaemon: cfg.Role is required")
	}
	if cfg.Iface == "" {
		return nil, fmt.Errorf("ifmgr.NewDaemon: cfg.Iface is required")
	}
	if cfg.ReconcileInterval == 0 {
		cfg.ReconcileInterval = 60 * time.Second
	}

	dlog := log.With("daemon", "ifmgr", "role", cfg.Role, "iface", cfg.Iface)
	dlog.Info(
		"ifmgr: NewDaemon entry",
		"reconcile_interval", cfg.ReconcileInterval.String(),
		"enable_dhcp", cfg.EnableDHCP,
		"enable_ra", cfg.EnableRA,
		"registered_modules", RegisteredNames(),
	)

	names, err := modulesForRole(cfg.Role)
	if err != nil {
		dlog.Warn("ifmgr: modulesForRole failed", "err", err)
		return nil, err
	}

	modules := make([]Module, 0, len(names))
	for _, name := range names {
		ctor, ok := Lookup(name)
		if !ok {
			dlog.Warn("ifmgr: module not registered", "module", name)
			return nil, fmt.Errorf(
				"ifmgr: role %q references module %q which is not registered "+
					"(registered: %v)", cfg.Role, name, RegisteredNames(),
			)
		}
		mcfg := cfg.ModuleConfigs[name]
		mod, err := ctor(mcfg)
		if err != nil {
			dlog.Warn("ifmgr: construct module failed", "module", name, "err", err)
			return nil, fmt.Errorf("construct module %q: %w", name, err)
		}
		dlog.Debug("ifmgr: module constructed", "module", name, "config_type", moduleConfigType(mcfg))
		modules = append(modules, mod)
	}

	d := &Daemon{
		cfg:          cfg,
		log:          dlog,
		role:         cfg.Role,
		modules:      modules,
		clock:        nil,
		env:          nil,
		reconcileReq: make(chan struct{}, 1),
		mu:           sync.Mutex{},
		startedAt:    time.Time{},
	}
	dlog.Info("ifmgr: Daemon ready", "module_count", len(modules))
	return d, nil
}

// Run starts the kernel monitor, optional DHCP+RA clients, calls Init on
// every module, runs an initial Reconcile, then enters the main loop.
// Blocks until ctx is cancelled. Returns the first error from Init or
// the main loop; transient Reconcile errors are logged but do not exit.
func (d *Daemon) Run(ctx context.Context) error {
	if d.clock == nil {
		d.clock = internalclock.Real{}
	}
	d.mu.Lock()
	d.startedAt = d.clock.Now()
	d.mu.Unlock()

	d.log.InfoContext(ctx, "ifmgr: Run entry")

	// Start the kernel event monitor first so any module Init that
	// triggers a netlink change (rare but possible) sees its own event.
	mon := netif.NewMonitor(ctx, d.log, netif.MonitorConfig{Iface: d.cfg.Iface})
	d.log.DebugContext(ctx, "ifmgr: monitor started")

	// Start DHCP client if requested.
	var dhcpClient *netif.DHCPClient
	if d.cfg.EnableDHCP {
		dhcpClient = netif.StartDHCPClient(ctx, d.log, netif.DHCPConfig{
			Iface:           d.cfg.Iface,
			InitialBackoff:  d.cfg.DHCPInitial,
			MaxBackoff:      d.cfg.DHCPMax,
			DiscoverTimeout: 0,
			RequestTimeout:  0,
			RenewTimeout:    0,
		})
		d.log.DebugContext(ctx, "ifmgr: DHCP client started")
	}

	// Open RA client if requested. Failure to open is non-fatal; modules
	// receive env.RA == nil and degrade to passive RA monitoring.
	var raClient *netif.RAClient
	if d.cfg.EnableRA {
		ra, err := netif.NewRAClient(d.cfg.Iface, d.log)
		if err != nil {
			d.log.WarnContext(ctx, "ifmgr: RAClient open failed; modules will operate in passive RA mode",
				"err", err)
		} else {
			raClient = ra
			d.log.DebugContext(ctx, "ifmgr: RA client opened",
				"link_local", ra.LinkLocal().String())
		}
	}

	d.env = &Env{
		Iface:            d.cfg.Iface,
		Sysctl:           netif.NewProcSysctlRunner(d.log, false),
		Log:              d.log,
		Alerts:           WrapNotifier(d.cfg.Notifier),
		Monitor:          mon,
		DHCP:             dhcpClient,
		RA:               raClient,
		RequestReconcile: d.requestReconcile,
		LiveState:        d.cfg.LiveState,
	}

	if err := d.initModules(ctx); err != nil {
		return err
	}

	return d.runLoop(ctx, mon, dhcpClient, raClient)
}

func (d *Daemon) initModules(ctx context.Context) error {
	// Init every module in role order. Failure here is fatal unless the
	// module returns ifmgr.ErrModuleDisabled, in which case the module
	// removes itself from this daemon's dispatch list for the rest of
	// the process lifetime. This is the opt-in pattern: a unified role
	// (e.g. "oob") can list modules that are only configured on some
	// hosts; an entirely-absent [ifmgr.modules.X] section yields a zero
	// Config, Init sees empty state, and returns the sentinel so the
	// daemon transparently skips the module on that host.
	enabledModules := make([]Module, 0, len(d.modules))
	for _, m := range d.modules {
		mlog := d.log.With("module", m.Name(), "phase", "init")
		mlog.DebugContext(ctx, "ifmgr: module Init")
		err := m.Init(ctx, d.env)
		if errors.Is(err, ErrModuleDisabled) {
			mlog.DebugContext(ctx, "ifmgr: module disabled, skipping for daemon lifetime",
				"reason", err.Error())
			continue
		}
		if err != nil {
			mlog.WarnContext(ctx, "ifmgr: module init failed", "err", err)
			return fmt.Errorf("module %s Init: %w", m.Name(), err)
		}
		enabledModules = append(enabledModules, m)
	}
	d.modules = enabledModules
	return nil
}

func (d *Daemon) runLoop(
	ctx context.Context,
	mon *netif.Monitor,
	dhcpClient *netif.DHCPClient,
	raClient *netif.RAClient,
) error {
	// Initial reconcile pass.
	initialCtx := tracing.WithOperation(ctx, "initial_reconcile")
	initialCtx, _ = tracing.StartTrace(initialCtx, "", "initial_reconcile")
	initLog := tracing.Logger(initialCtx, d.log).With("phase", "initial-reconcile")
	d.reconcileAll(initialCtx, initLog)

	tick := time.NewTicker(d.cfg.ReconcileInterval)
	defer tick.Stop()

	d.log.InfoContext(ctx, "ifmgr: entering main loop")
	for {
		select {
		case <-ctx.Done():
			d.log.DebugContext(ctx, "ifmgr: ctx cancelled; exiting (kernel state preserved)")
			if raClient != nil {
				_ = raClient.Close()
			}
			return nil

		case ev, ok := <-mon.Events:
			if !ok {
				d.log.WarnContext(ctx, "ifmgr: monitor events channel closed")
				continue
			}
			eventCtx := tracing.WithOperation(ctx, "kernel_event")
			eventCtx = tracing.WithEvent(eventCtx, ev.Kind.String())
			eventCtx = tracing.WithAttrs(
				eventCtx,
				slog.String("iface", ev.Iface),
			)
			eventCtx, _ = tracing.StartTrace(eventCtx, "", "kernel_event")
			elog := tracing.Logger(eventCtx, d.log).With(
				"phase", "kernel-event",
				"kind", ev.Kind.String(),
				"iface", ev.Iface,
			)
			d.dispatchEvent(eventCtx, elog, ev)

		case lease, ok := <-dhcpEvents(dhcpClient):
			if !ok {
				continue
			}
			leaseCtx := tracing.WithOperation(ctx, "dhcp_event")
			leaseCtx = tracing.WithEvent(leaseCtx, lease.State.String())
			leaseCtx, _ = tracing.StartTrace(leaseCtx, "", "dhcp_event")
			llog := tracing.Logger(leaseCtx, d.log).With(
				"phase", "dhcp-event",
				"state", lease.State.String(),
			)
			d.dispatchLease(leaseCtx, llog, lease)

		case <-tick.C:
			tickCtx := tracing.WithOperation(ctx, "periodic_reconcile")
			tickCtx, _ = tracing.StartTrace(tickCtx, "", "periodic_reconcile")
			tlog := tracing.Logger(tickCtx, d.log).With("phase", "periodic-reconcile")
			tlog.DebugContext(tickCtx, "ifmgr: tick")
			d.reconcileAll(tickCtx, tlog)
			d.evaluateAlertsAll(tickCtx, tlog, d.clock.Now())

		case <-d.reconcileReq:
			reqCtx := tracing.WithOperation(ctx, "requested_reconcile")
			reqCtx, _ = tracing.StartTrace(reqCtx, "", "requested_reconcile")
			rlog := tracing.Logger(reqCtx, d.log).With("phase", "requested-reconcile")
			rlog.DebugContext(reqCtx, "ifmgr: reconcile on request")
			d.reconcileAll(reqCtx, rlog)
			d.evaluateAlertsAll(reqCtx, rlog, d.clock.Now())
		}
	}
}

// requestReconcile asks the main loop to run a reconcile pass promptly. The
// send is non-blocking: reconcileReq is buffered with capacity 1, so when a
// pass is already queued the request coalesces into it rather than blocking the
// caller. Modules call this via Env.RequestReconcile after a state change that
// downstream modules must act on now, for example a health transition that
// should drive an immediate wan.routes failover instead of waiting for the tick.
func (d *Daemon) requestReconcile(reason string) {
	select {
	case d.reconcileReq <- struct{}{}:
		d.log.Debug("ifmgr: reconcile requested", "reason", reason)
	default:
		d.log.Debug("ifmgr: reconcile already queued; coalescing", "reason", reason)
	}
}

// reconcileAll runs Reconcile on every module in role order. A module
// returning an error is logged at WARN; the loop continues so that one
// flaky module does not silence the others.
func (d *Daemon) reconcileAll(ctx context.Context, log *slog.Logger) {
	for _, m := range d.modules {
		mlog := log.With("module", m.Name())
		mlog.DebugContext(ctx, "ifmgr: Reconcile")
		if err := m.Reconcile(ctx, mlog); err != nil {
			mlog.WarnContext(ctx, "ifmgr: module Reconcile failed", "err", err)
		}
	}
}

// dispatchEvent fans out one kernel event to every module.
func (d *Daemon) dispatchEvent(ctx context.Context, log *slog.Logger, ev netif.Event) {
	for _, m := range d.modules {
		mlog := log.With("module", m.Name())
		if err := m.OnKernelEvent(ctx, mlog, ev); err != nil {
			mlog.WarnContext(ctx, "ifmgr: module OnKernelEvent failed", "err", err)
		}
	}
}

// dispatchLease fans out one DHCP lease event to every module.
func (d *Daemon) dispatchLease(ctx context.Context, log *slog.Logger, lease netif.LeaseInfo) {
	for _, m := range d.modules {
		mlog := log.With("module", m.Name())
		if err := m.OnDHCPLease(ctx, mlog, lease); err != nil {
			mlog.WarnContext(ctx, "ifmgr: module OnDHCPLease failed", "err", err)
		}
	}
}

// evaluateAlertsAll runs EvaluateAlerts on every module.
func (d *Daemon) evaluateAlertsAll(ctx context.Context, log *slog.Logger, now time.Time) {
	for _, m := range d.modules {
		mlog := log.With("module", m.Name())
		m.EvaluateAlerts(ctx, mlog, now)
	}
}

// dhcpEvents returns the lease events channel of c, or a nil channel
// (which blocks forever in select) when c is nil. Lets us write a single
// select arm regardless of whether DHCP is enabled.
func dhcpEvents(c *netif.DHCPClient) <-chan netif.LeaseInfo {
	if c == nil {
		return nil
	}
	return c.Events
}

// mapKeys returns the sorted key set of m. Used for diagnostic logging
// of module config trees without dumping potentially-large values.
func moduleConfigType(cfg ModuleConfig) string {
	if cfg == nil {
		return "<nil>"
	}
	return cfg.ModuleConfigName()
}
