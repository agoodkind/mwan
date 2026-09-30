// Package links reconciles MWAN-owned kernel links beside legacy interfaces.
package links

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const moduleName = "links"

// Config supplies link intent and the durable ownership record path.
type Config struct {
	Connections []interfaceintent.Connection
	StateFile   string
}

// ModuleConfigName selects the registered link module.
func (Config) ModuleConfigName() string { return moduleName }

// Module applies link intent without changing legacy-owned connections.
type Module struct {
	ifmgr.BaseModule
	connections []interfaceintent.Connection
	reconciler  *netif.OwnedLinkReconciler
	observed    map[string]bool
}

// New validates link ownership before the daemon writes networkd files.
func New(config ifmgr.ModuleConfig) (ifmgr.Module, error) {
	module := &Module{BaseModule: ifmgr.NewBaseModule(moduleName), connections: nil, reconciler: nil, observed: nil}
	if config == nil {
		return module, nil
	}
	settings, ok := config.(Config)
	if !ok {
		return nil, fmt.Errorf("links: invalid config type %T", config)
	}
	module.connections = settings.Connections
	owned := 0
	for _, connection := range settings.Connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		owned++
		if connection.ID == "" || connection.Link == nil ||
			connection.LeaseStore != "" || len(connection.Networkd) != 0 {
			return nil, fmt.Errorf("links: connection %s has unsupported owned intent", connection.Name)
		}
	}
	if owned == 0 && settings.StateFile == "" {
		return module, nil
	}
	if !filepath.IsAbs(settings.StateFile) {
		return nil, fmt.Errorf("links: state_file must be an absolute path when MWAN owns or removes links")
	}
	reconciler, err := netif.NewOwnedLinkReconciler(settings.StateFile)
	if err != nil {
		slog.Error("links: open ownership state failed", "path", settings.StateFile, "err", err)
		return nil, fmt.Errorf("links: open ownership state: %w", err)
	}
	module.reconciler = reconciler
	module.observed = observedLinks(settings.Connections)
	return module, nil
}

func observedLinks(connections []interfaceintent.Connection) map[string]bool {
	observed := make(map[string]bool)
	for _, connection := range connections {
		if connection.Owner != interfaceintent.OwnerMWAN || connection.Link == nil {
			continue
		}
		observed[connection.Name] = true
		if connection.Link.VLAN != nil {
			observed[connection.Link.VLAN.Parent] = true
		}
		if connection.Link.BridgeMaster != "" {
			observed[connection.Link.BridgeMaster] = true
		}
	}
	return observed
}

// Init subscribes to link changes that can satisfy pending dependencies.
func (module *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := module.InitBase(env, "module", moduleName)
	if module.reconciler == nil {
		return ifmgr.ErrModuleDisabled
	}
	env.OwnedLinks = &ifmgr.OwnedLinkResults{}
	names := make([]string, 0, len(module.observed))
	for name := range module.observed {
		names = append(names, name)
	}
	sort.Strings(names)
	ifmgr.StartIfaceMonitors(ctx, log, moduleName, names, env.Connections, module.onObservedEvent)
	return nil
}

func (module *Module) onObservedEvent(_ context.Context, _ *slog.Logger, event netif.Event) {
	if module.Env.RequestReconcile != nil && module.observed[event.Iface] {
		module.Env.RequestReconcile("link event on " + event.Iface)
	}
}

// OnKernelEvent requests reconciliation after a watched link changes.
func (module *Module) OnKernelEvent(ctx context.Context, log *slog.Logger, event netif.Event) error {
	module.onObservedEvent(ctx, log, event)
	return nil
}

// Reconcile applies owned links and publishes per-connection results.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	module.Env.OwnedLinks.Replace(nil)
	results, err := module.reconciler.Reconcile(ctx, log, module.connections)
	if err != nil {
		log.ErrorContext(ctx, "links: reconcile pass failed", "err", err)
		return fmt.Errorf("links: reconcile: %w", err)
	}
	module.Env.OwnedLinks.Replace(results)
	for _, result := range results {
		reason := ""
		if result.Err != nil {
			reason = result.Err.Error()
		}
		if result.Status == netif.OwnedLinkFailed && result.Err != nil {
			log.WarnContext(ctx, "links: reconcile failed", "connection_id", result.ConnectionID, "operation", result.Operation, "err", result.Err)
		}
		if module.Env.LiveState != nil {
			module.Env.LiveState.SetApplyResult(result.ConnectionID, wanstate.ApplyResult{
				At:         time.Time{},
				Operation:  result.Operation,
				Dependency: result.Dependency,
				Result:     string(result.Status),
				Reason:     reason,
			})
		}
	}
	return nil
}

func init() { ifmgr.Register(moduleName, New) }
