package ifmgr

import (
	"context"
	"log/slog"
	"net/netip"
	"sort"
	"sync"
	"time"

	"goodkind.io/mwan/internal/interfaceintent"
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
	// then on every reconcile tick. Modules run in role order and MUST be
	// idempotent. A module that requires an earlier result must verify it
	// before changing kernel state.
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
// Modules must check optional role capabilities before using them.
type Env struct {
	// Iface is the interface name the role manages. Modules that operate
	// on multiple ifaces (future) will get a per-iface Env.
	Iface string
	// Connections are the configured interface identities available to modules.
	Connections []interfaceintent.Connection
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
	// DHCPRecoveryPending defers startup pruning during saved lease validation.
	DHCPRecoveryPending bool
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
	// OwnedLinks contains the latest complete link reconcile result for this pass.
	OwnedLinks *OwnedLinkResults
	// OwnedAddresses contains address installation results from the current pass.
	OwnedAddresses *OwnedAddressResults
	// Delegations publishes MWAN-owned DHCPv6 addresses and prefixes.
	Delegations *netif.DHCPv6PDStore
	// PrepareLocalIPv6 protects new local DHCPv6 addresses from forwarding translation before installation.
	PrepareLocalIPv6 func(context.Context, *slog.Logger, string, []netip.Addr) error
}

// OwnedAddressResults shares successful exact address writes with translation consumers.
type OwnedAddressResults struct {
	mu        sync.RWMutex
	addresses map[string]map[string]bool
	families  map[string]map[string]bool
	applied   map[string]map[string]bool
}

// Replace clears results before an address reconciliation pass.
func (s *OwnedAddressResults) Replace() {
	s.mu.Lock()
	s.addresses = make(map[string]map[string]bool)
	s.families = make(map[string]map[string]bool)
	s.applied = make(map[string]map[string]bool)
	s.mu.Unlock()
}

// Set records a verified address for one connection.
func (s *OwnedAddressResults) Set(id, prefix string) {
	s.mu.Lock()
	if s.addresses[id] == nil {
		s.addresses[id] = make(map[string]bool)
	}
	s.addresses[id][prefix] = true
	s.mu.Unlock()
}

// Has reports an address installed in the current pass.
func (s *OwnedAddressResults) Has(id, prefix string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.addresses[id][prefix]
}

// SetFamilyReady records verified application of every owned object in a family.
func (s *OwnedAddressResults) SetFamilyReady(id, family string) {
	s.mu.Lock()
	if s.families[id] == nil {
		s.families[id] = make(map[string]bool)
	}
	s.families[id][family] = true
	s.mu.Unlock()
}

// FamilyReady reports complete application in the current pass.
func (s *OwnedAddressResults) FamilyReady(id, family string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.families[id][family]
}

// SetFamilyApplied records a successful kernel address and route reconciliation.
func (s *OwnedAddressResults) SetFamilyApplied(id, family string) {
	s.mu.Lock()
	if s.applied[id] == nil {
		s.applied[id] = make(map[string]bool)
	}
	s.applied[id][family] = true
	s.mu.Unlock()
}

// FamilyApplied reports whether the family writer succeeded in this pass.
func (s *OwnedAddressResults) FamilyApplied(id, family string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.applied[id][family]
}

// OwnedLinkResults shares verified link identities with later modules.
type OwnedLinkResults struct {
	mu      sync.RWMutex
	results map[string]netif.OwnedLinkResult
}

// Replace starts a new complete link result set.
func (s *OwnedLinkResults) Replace(results []netif.OwnedLinkResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results = make(map[string]netif.OwnedLinkResult, len(results))
	for _, result := range results {
		s.results[result.ConnectionID] = result
	}
}

// Get returns the latest result for one connection.
func (s *OwnedLinkResults) Get(id string) (netif.OwnedLinkResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.results[id]
	return result, ok
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
