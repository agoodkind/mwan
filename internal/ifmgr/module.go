package ifmgr

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

// Module is one feature plug-in for the ifmgr daemon. Each role
// (oob, failover, ...) selects an ordered list of modules; the daemon
// instantiates them via their Constructor and dispatches lifecycle
// calls into them.
//
// All methods MUST be safe to call concurrently with each other on a
// single Module instance: Reconcile and OnKernelEvent in particular run
// in different goroutines.
type Module interface {
	// Name returns the stable module identifier used in logs and config
	// (e.g. "oobv6", "slaac_health"). Must match the registry key.
	Name() string

	// Init is called once before the daemon enters its main loop. The
	// module receives an Env granting access to the shared netif primitives,
	// the alert manager, and a logger pre-bound with the module name.
	// Returning a non-nil error fails the daemon startup.
	Init(ctx context.Context, env *Env) error

	// Reconcile is called once at startup (after Init for all modules) and
	// then on every reconcile tick. Modules MUST be idempotent and tolerant
	// of running in any order relative to other modules registered for the
	// same role.
	Reconcile(ctx context.Context, log *slog.Logger) error

	// OnKernelEvent is called for every netif.Event that arrives from the
	// monitor. Modules filter by interface and event kind themselves.
	OnKernelEvent(ctx context.Context, log *slog.Logger, ev netif.Event) error

	// OnDHCPLease is called for every DHCPv4 lease state change emitted by
	// the daemon's DHCP client (if any; nil-safe to ignore in modules that
	// do not care about DHCP).
	OnDHCPLease(ctx context.Context, log *slog.Logger, lease netif.LeaseInfo) error

	// EvaluateAlerts is called on every reconcile tick after Reconcile. The
	// module inspects its own state, decides whether any alert should fire,
	// and emits via env.Alerts. The argument is the wall-clock now used by
	// the dispatcher so that modules and the alert manager agree on time.
	EvaluateAlerts(ctx context.Context, log *slog.Logger, now time.Time)
}

// ModuleConfig is the typed runtime config surface each module constructor reads.
type ModuleConfig interface {
	ModuleConfigName() string
}

// ModuleConfigSet maps module names to their typed runtime configs.
type ModuleConfigSet map[string]ModuleConfig

// Constructor builds a Module from its typed runtime config. Config-file
// parsing happens in internal/config plus the cmd/mwan ifmgr adapter; the
// registry only sees the runtime shape it needs to instantiate a module.
type Constructor func(cfg ModuleConfig) (Module, error)

// Env is everything the daemon hands a module at Init time. Modules MUST
// hold a reference to the bits they need; the daemon will not pass Env
// again on subsequent dispatch calls.
//
// All fields are non-nil except DHCP, which is nil if the role's iface
// section has dhcp_v4 = false. Modules that only run when DHCP is
// configured should defensively check.
type Env struct {
	// Iface is the interface name the role manages. Modules that operate
	// on multiple ifaces (future) will get a per-iface Env.
	Iface string
	// Sysctl exposes /proc/sys read+write. Writes require systemd
	// ReadWritePaths or relaxed ProtectKernelTunables; the daemon
	// surfaces EACCES with a helpful message.
	Sysctl netif.SysctlRunner
	// Log is pre-bound with module=<Name>; modules chain .With(...) for
	// per-operation context.
	Log *slog.Logger
	// Alerts is the role-shared alert manager. EvaluateAlerts callers
	// invoke Notify*/Evaluate* methods on it.
	Alerts *AlertManager
	// Monitor is the netif kernel event monitor. Modules that need to
	// observe link state or filter on Event.Kind themselves can subscribe
	// indirectly via the daemon's OnKernelEvent fan-out.
	Monitor *netif.Monitor
	// DHCP is the DHCPv4 client, or nil when the iface section did not
	// request dhcp_v4.
	DHCP *netif.DHCPClient
	// RA is the Router Solicitation client, or nil when the iface section
	// did not request ra_solicit.
	RA *netif.RAClient
	// RequestReconcile asks the daemon to run a reconcile pass promptly
	// instead of waiting for the periodic tick. A module calls it after a
	// state change that a downstream module must act on now, for example a
	// health transition that should drive an immediate wan.routes failover.
	// The call is non-blocking and coalescing. It is nil in unit tests that
	// construct an Env without a daemon; callers must nil-check.
	RequestReconcile func(reason string)
	// LiveState is the snapshot store the management surface serves from.
	// Modules write what they just reconciled; the operational-datastore
	// provider reads it at request time. It is nil when the host does not
	// publish a management surface; writers must nil-check, and a nil
	// store costs the reconcile path nothing.
	LiveState *wanstate.Store
}

// registry maps module name to constructor. Populated at package init
// time from each module subpackage's init() via Register.
var (
	registryMu sync.RWMutex
	registry   = map[string]Constructor{}
)

// Register makes a module constructor available to the daemon. Each
// module package's init() should call this exactly once. Panics on
// duplicate registration so configuration errors surface at startup
// rather than silently shadowing.
func Register(name string, ctor Constructor) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[name]; exists {
		slog.Default().Error("ifmgr: duplicate module registration",
			"module", name)
		return
	}
	registry[name] = ctor
}

// Lookup returns a constructor by name. Returns ok=false if the module
// was never registered.
func Lookup(name string) (Constructor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	c, ok := registry[name]
	return c, ok
}

// RegisteredNames returns a sorted list of all registered module names.
// Used by diagnostic logging at startup so operators can see what
// modules are linked into the binary.
func RegisteredNames() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
