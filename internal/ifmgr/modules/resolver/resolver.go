// Package resolver applies configured DNS and search domains to owned links.
package resolver

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/resolved"
)

const moduleName = "resolver"

// Config supplies static resolver intent and its durable journal path.
type Config struct {
	Connections []interfaceintent.Connection
	StateFile   string
}

// ModuleConfigName identifies the resolver module.
func (Config) ModuleConfigName() string { return moduleName }

// Module reconciles static resolver fields independently of address acquisition.
type Module struct {
	ifmgr.BaseModule
	mu          sync.Mutex
	connections []interfaceintent.Connection
	ownership   *resolved.Ownership
}

// New loads the resolver journal before the module starts.
func New(config ifmgr.ModuleConfig) (ifmgr.Module, error) {
	settings := Config{Connections: nil, StateFile: ""}
	if config != nil {
		var ok bool
		settings, ok = config.(Config)
		if !ok {
			return nil, fmt.Errorf("resolver: invalid config type %T", config)
		}
	}
	module := &Module{BaseModule: ifmgr.NewBaseModule(moduleName), mu: sync.Mutex{}, connections: settings.Connections, ownership: nil}
	if settings.StateFile == "" && !hasStaticSettings(settings.Connections) {
		return module, nil
	}
	ownership, err := resolved.NewOwnership(settings.StateFile)
	if err != nil {
		slog.Warn("resolver: load ownership failed", "err", err)
		return nil, fmt.Errorf("resolver ownership: %w", err)
	}
	module.ownership = ownership
	return module, nil
}

func hasStaticSettings(connections []interfaceintent.Connection) bool {
	for _, connection := range connections {
		if connection.Owner != interfaceintent.OwnerMWAN || connection.Enabled != nil && !*connection.Enabled {
			continue
		}
		intent := aggregate(connection)
		if len(intent.DNS)+len(intent.Domains) != 0 {
			return true
		}
	}
	return false
}

// Init requires link results when enabled MWAN connections remain configured.
func (module *Module) Init(_ context.Context, env *ifmgr.Env) error {
	module.InitBase(env, "module", moduleName)
	if module.ownership == nil {
		return ifmgr.ErrModuleDisabled
	}
	if env.OwnedLinks == nil && hasOwnedConnections(module.connections) {
		return fmt.Errorf("resolver: owned link results are required")
	}
	return nil
}

func hasOwnedConnections(connections []interfaceintent.Connection) bool {
	for _, connection := range connections {
		if connection.Owner == interfaceintent.OwnerMWAN && (connection.Enabled == nil || *connection.Enabled) {
			return true
		}
	}
	return false
}

// Reconcile applies configured settings or restores fields removed from intent.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	module.mu.Lock()
	defer module.mu.Unlock()
	intents := make([]resolved.Intent, 0, len(module.connections))
	for _, connection := range module.connections {
		if connection.Owner != interfaceintent.OwnerMWAN || connection.Enabled != nil && !*connection.Enabled {
			continue
		}
		intent := aggregate(connection)
		link, ok := module.Env.OwnedLinks.Get(connection.ID.String())
		if ok && link.Status == netif.OwnedLinkReady && link.IfIndex > 0 && link.ActualName != "" {
			intent.Ready = true
			intent.Index = link.IfIndex
			intent.Name = link.ActualName
		}
		intents = append(intents, intent)
	}
	if err := module.ownership.Reconcile(ctx, intents); err != nil {
		log.WarnContext(ctx, "resolver application failed", "operation", "reconcile", "result", "failed")
		return fmt.Errorf("apply resolver configuration: %w", err)
	}
	return nil
}

func aggregate(connection interfaceintent.Connection) resolved.Intent {
	intent := resolved.Intent{ConnectionID: connection.ID.String(), Name: connection.Name, Index: 0, Ready: false, DNS: nil, Domains: nil}
	addresses := map[netip.Addr]bool{}
	domains := map[string]bool{}
	appendFamily := func(family interfaceintent.Family) {
		if family.Enabled != nil && !*family.Enabled {
			return
		}
		for _, address := range family.DNS {
			address = address.Unmap()
			if addresses[address] {
				continue
			}
			addresses[address] = true
			server := resolved.DNS{Family: 10, Address: address.AsSlice(), Port: 0, ServerName: ""}
			if address.Is4() {
				server.Family = 2
			}
			intent.DNS = append(intent.DNS, server)
		}
		for _, domain := range family.SearchDomains {
			if domains[domain] {
				continue
			}
			domains[domain] = true
			intent.Domains = append(intent.Domains, resolved.Domain{Name: domain, RoutingOnly: false})
		}
	}
	if connection.IPv4 != nil {
		appendFamily(connection.IPv4.Family)
	}
	if connection.IPv6 != nil {
		appendFamily(connection.IPv6.Family)
	}
	return intent
}

func init() { ifmgr.Register(moduleName, New) }
