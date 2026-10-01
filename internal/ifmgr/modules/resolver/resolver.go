// Package resolver applies configured and acquired DNS to owned links.
package resolver

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/resolved"
)

const moduleName = "resolver"

// Config supplies resolver intent and its durable journal path.
type Config struct {
	Connections []interfaceintent.Connection
	StateFile   string
}

// ModuleConfigName identifies the resolver module.
func (Config) ModuleConfigName() string { return moduleName }

// Module combines configured DNS with valid acquired DNS in one resolver journal.
type Module struct {
	ifmgr.BaseModule
	mu          sync.Mutex
	connections []interfaceintent.Connection
	ownership   *resolved.Ownership
	ra          map[string]*raSession
	clock       clock.Clock
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
	module := &Module{BaseModule: ifmgr.NewBaseModule(moduleName), mu: sync.Mutex{}, connections: settings.Connections, ownership: nil, ra: map[string]*raSession{}, clock: clock.Real{}}
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
		if len(intent.DNS)+len(intent.Domains) != 0 || wantsRADNS(connection) || wantsDHCPDNS(connection) {
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

// Reconcile applies current resolver contributions or restores removed fields.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	module.mu.Lock()
	defer module.mu.Unlock()
	intents := make([]resolved.Intent, 0, len(module.connections))
	activeRA := map[string]bool{}
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
			appendDNS(&intent, module.leaseDNS(connection, link.ActualName))
			if wantsRADNS(connection) {
				id := connection.ID.String()
				activeRA[id] = true
				appendDNS(&intent, module.routerDNS(ctx, id, link, log))
			}
		}
		intents = append(intents, intent)
	}
	for id, session := range module.ra {
		if !activeRA[id] {
			session.cancel()
			delete(module.ra, id)
		}
	}
	if err := module.ownership.Reconcile(ctx, intents); err != nil {
		log.WarnContext(ctx, "resolver application failed", "operation", "reconcile", "result", "failed")
		return fmt.Errorf("apply resolver configuration: %w", err)
	}
	return nil
}

func (module *Module) leaseDNS(connection interfaceintent.Connection, name string) []netip.Addr {
	if !wantsDHCPDNS(connection) || module.Env.Delegations == nil {
		return nil
	}
	lease, found := module.Env.Delegations.Get(name)
	if !found || !lease.UseDNS {
		return nil
	}
	now := module.clock.Now()
	for _, prefix := range lease.Prefixes {
		if now.Before(prefix.ValidUntil) {
			return lease.DNS
		}
	}
	for _, address := range lease.Addresses {
		if now.Before(address.ValidUntil) {
			return lease.DNS
		}
	}
	return nil
}

func wantsRADNS(connection interfaceintent.Connection) bool {
	return connection.IPv6 != nil && (connection.IPv6.Enabled == nil || *connection.IPv6.Enabled) && connection.IPv6.UseRADNS != nil && *connection.IPv6.UseRADNS
}

func wantsDHCPDNS(connection interfaceintent.Connection) bool {
	return connection.IPv6 != nil && (connection.IPv6.Enabled == nil || *connection.IPv6.Enabled) && connection.IPv6.DHCPv6 != nil && connection.IPv6.DHCPv6.UseDNS != nil && *connection.IPv6.DHCPv6.UseDNS
}

func appendDNS(intent *resolved.Intent, addresses []netip.Addr) {
	for _, address := range addresses {
		present := false
		for _, server := range intent.DNS {
			if existing, ok := netip.AddrFromSlice(server.Address); ok && existing == address {
				present = true
				break
			}
		}
		if !present {
			intent.DNS = append(intent.DNS, resolved.DNS{Family: 10, Address: address.AsSlice(), Port: 0, ServerName: ""})
		}
	}
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
