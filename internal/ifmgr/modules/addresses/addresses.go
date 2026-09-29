// Package addresses reconciles static addresses and main-table defaults on MWAN links.
package addresses

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const moduleName = "addresses"

// Config supplies static family intent and the durable journal path.
type Config struct {
	Connections []interfaceintent.Connection
	StateFile   string
}

// ModuleConfigName selects the registered address module.
func (Config) ModuleConfigName() string { return moduleName }

// Module applies static family intent after links are ready.
type Module struct {
	ifmgr.BaseModule
	connections []interfaceintent.Connection
	reconciler  *netif.OwnedStaticReconciler
}

// New validates the address journal before daemon startup.
func New(config ifmgr.ModuleConfig) (ifmgr.Module, error) {
	module := &Module{BaseModule: ifmgr.NewBaseModule(moduleName), connections: nil, reconciler: nil}
	if config == nil {
		return module, nil
	}
	settings, ok := config.(Config)
	if !ok {
		return nil, fmt.Errorf("addresses: invalid config type %T", config)
	}
	module.connections = settings.Connections
	if settings.StateFile == "" {
		for _, connection := range settings.Connections {
			if connection.Owner == interfaceintent.OwnerMWAN && (connection.IPv4 != nil || connection.IPv6 != nil) {
				return nil, fmt.Errorf("addresses: state_file is required for owned static families")
			}
		}
		return module, nil
	}
	if !filepath.IsAbs(settings.StateFile) {
		return nil, fmt.Errorf("addresses: state_file must be absolute")
	}
	reconciler, err := netif.NewOwnedStaticReconciler(settings.StateFile)
	if err != nil {
		slog.Warn("addresses: open ownership state failed", "path", settings.StateFile, "err", err)
		return nil, fmt.Errorf("addresses: open ownership state: %w", err)
	}
	module.reconciler = reconciler
	return module, nil
}

// Init requires the preceding link module's result store.
func (module *Module) Init(_ context.Context, env *ifmgr.Env) error {
	module.InitBase(env, "module", moduleName)
	if module.reconciler == nil {
		return ifmgr.ErrModuleDisabled
	}
	if env.OwnedLinks == nil {
		return fmt.Errorf("addresses: links module is required")
	}
	return nil
}

// Reconcile applies each family and then removes journaled deleted families.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	desiredFamilies := make(map[string]map[string]bool)
	for _, connection := range module.connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		families := make(map[string]bool)
		for _, candidate := range []struct {
			name   string
			intent *interfaceintent.Family
		}{
			{name: "ipv4", intent: familyV4(connection.IPv4)},
			{name: "ipv6", intent: familyV6(connection.IPv6)},
		} {
			if candidate.intent == nil {
				continue
			}
			families[candidate.name] = true
			module.reconcileFamily(ctx, log, connection, candidate.name, *candidate.intent)
		}
		desiredFamilies[connection.ID.String()] = families
	}
	err := module.reconciler.PruneRemoved(desiredFamilies)
	var pending []wanstate.PendingRemoval
	if err != nil {
		var failure *netif.OwnedStaticPruneError
		if errors.As(err, &failure) && module.Env.LiveState != nil {
			result := wanstate.ApplyResult{Operation: "prune-static", Dependency: "link", Result: "failed", Reason: failure.Error(), At: time.Time{}}
			if _, configured := desiredFamilies[failure.ConnectionID]; configured {
				module.Env.LiveState.SetFamilyApplyResult(failure.ConnectionID, failure.Family, result)
			} else {
				pending = append(pending, wanstate.PendingRemoval{ConnectionID: failure.ConnectionID, Name: failure.LinkName, Family: failure.Family, Apply: result})
			}
		}
	}
	if module.Env.LiveState != nil {
		module.Env.LiveState.ReplacePendingRemovals(pending)
	}
	if err != nil {
		log.WarnContext(ctx, "addresses: prune removed families failed", "err", err)
		return fmt.Errorf("addresses: prune removed families: %w", err)
	}
	return nil
}

func (module *Module) reconcileFamily(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection, family string, settings interfaceintent.Family) {
	id := connection.ID.String()
	assignments := make([]interfaceintent.Assignment, 0, len(settings.Addresses)+1)
	for _, address := range settings.Addresses {
		assignments = append(assignments, staticAssignment(connection, family, "configured", interfaceintent.PurposeLocal, address.Prefix))
	}
	if settings.Gateway.IsValid() {
		destination := netip.MustParsePrefix("::/0")
		if family == "ipv4" {
			destination = netip.MustParsePrefix("0.0.0.0/0")
		}
		metric := uint32(0)
		if settings.RouteMetric != nil {
			metric = *settings.RouteMetric
		}
		assignments = append(assignments, interfaceintent.Assignment{
			ConnectionID: connection.ID, Family: family, Kind: interfaceintent.AssignmentStaticRoute,
			Source: "configured", Purpose: "", Value: netip.Prefix{}, Route: &interfaceintent.RouteIntent{
				Destination: destination,
				Gateway:     settings.Gateway, TableID: 254, Metric: metric,
			}, ClientID: "", DUID: "", IAID: nil, AcquiredAt: time.Time{}, RenewAt: nil,
			RebindAt: nil, PreferredUntil: nil, ValidUntil: nil, Valid: true,
		})
	}
	if module.Env.LiveState != nil {
		module.Env.LiveState.SetAssignment(id, family, "static", "valid", assignments)
	}
	ready, ok := module.Env.OwnedLinks.Get(id)
	var err error
	if !ok {
		err = fmt.Errorf("current link result is absent")
	} else {
		err = module.reconciler.ReconcileFamily(ctx, connection, family, settings, ready)
	}
	result := "ready"
	reason := ""
	if err != nil {
		result = "failed"
		reason = err.Error()
		log.WarnContext(ctx, "addresses: reconcile family failed", "connection_id", id, "family", family, "err", err)
	}
	if module.Env.LiveState != nil {
		module.Env.LiveState.SetFamilyApplyResult(id, family, wanstate.ApplyResult{Operation: "reconcile-static", Dependency: "link", Result: result, Reason: reason, At: time.Time{}})
	}
}

func staticAssignment(connection interfaceintent.Connection, family, source string, purpose interfaceintent.AddressPurpose, value netip.Prefix) interfaceintent.Assignment {
	return interfaceintent.Assignment{
		ConnectionID: connection.ID, Family: family, Kind: interfaceintent.AssignmentStatic,
		Source: source, Purpose: purpose, Value: value, Route: nil, ClientID: "", DUID: "", IAID: nil,
		AcquiredAt: time.Time{}, RenewAt: nil, RebindAt: nil, PreferredUntil: nil, ValidUntil: nil, Valid: true,
	}
}

func familyV4(value *interfaceintent.IPv4) *interfaceintent.Family {
	if value == nil {
		return nil
	}
	return &value.Family
}

func familyV6(value *interfaceintent.IPv6) *interfaceintent.Family {
	if value == nil {
		return nil
	}
	return &value.Family
}

func init() { ifmgr.Register(moduleName, New) }
