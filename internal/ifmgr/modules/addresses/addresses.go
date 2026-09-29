// Package addresses reconciles static and translation addresses on MWAN links.
package addresses

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"slices"
	"time"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/wanstate"
)

const moduleName = "addresses"

// Config supplies owned family intent, translation settings, and the journal path.
type Config struct {
	Connections []interfaceintent.Connection
	Providers   map[string]Provider
	StateFile   string
}

// Provider supplies translation addresses for an exclusively owned connection.
type Provider struct {
	IPv4 *config.IPv4Translation
	IPv6 *config.IPv6Translation
}

// ModuleConfigName selects the registered address module.
func (Config) ModuleConfigName() string { return moduleName }

// Module applies static family intent after links are ready.
type Module struct {
	ifmgr.BaseModule
	connections []interfaceintent.Connection
	providers   map[string]Provider
	reconciler  *netif.OwnedStaticReconciler
}

// New validates the address journal before daemon startup.
func New(config ifmgr.ModuleConfig) (ifmgr.Module, error) {
	module := &Module{BaseModule: ifmgr.NewBaseModule(moduleName), connections: nil, providers: nil, reconciler: nil}
	if config == nil {
		return module, nil
	}
	settings, ok := config.(Config)
	if !ok {
		return nil, fmt.Errorf("addresses: invalid config type %T", config)
	}
	module.connections = settings.Connections
	module.providers = settings.Providers
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
	env.OwnedAddresses = &ifmgr.OwnedAddressResults{}
	return nil
}

// Reconcile applies owned addresses and routes, then removes deleted families.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	module.Env.OwnedAddresses.Replace()
	desiredFamilies := make(map[string]map[string]bool)
	for _, connection := range module.connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		families := make(map[string]bool)
		provider := module.providers[connection.ID.String()]
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
			settings := *candidate.intent
			assignments := make([]interfaceintent.Assignment, 0, len(settings.Addresses)+1)
			for _, address := range settings.Addresses {
				assignments = append(assignments, addressAssignment(connection, candidate.name, interfaceintent.AssignmentStatic, address.Purpose, address.Prefix))
			}
			if candidate.name == "ipv4" {
				for _, address := range mappedAddresses(connection, provider.IPv4) {
					settings.Addresses = append(slices.Clone(settings.Addresses), address)
					assignments = append(assignments, addressAssignment(connection, candidate.name, interfaceintent.AssignmentMapped, address.Purpose, address.Prefix))
				}
			} else if prefix := nptAddress(provider.IPv6); prefix.IsValid() {
				settings.Addresses = append(slices.Clone(settings.Addresses), interfaceintent.Address{Prefix: prefix, Purpose: interfaceintent.PurposeForward})
				assignments = append(assignments, addressAssignment(connection, candidate.name, interfaceintent.AssignmentNPTExternal, interfaceintent.PurposeForward, prefix))
			}
			module.reconcileFamily(ctx, log, connection, candidate.name, settings, assignments)
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

func (module *Module) reconcileFamily(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection, family string, settings interfaceintent.Family, assignments []interfaceintent.Assignment) {
	id := connection.ID.String()
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
	if err == nil {
		err = module.publishInstalled(ctx, log, connection, settings, ready)
	}
	if err == nil {
		module.Env.OwnedAddresses.SetFamilyReady(id, family)
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

func (module *Module) publishInstalled(ctx context.Context, log *slog.Logger, connection interfaceintent.Connection, settings interfaceintent.Family, ready netif.OwnedLinkResult) error {
	link, err := netlink.LinkByName(ready.ActualName)
	if err != nil || link.Attrs().Index != ready.IfIndex {
		return fmt.Errorf("connection %s link identity changed during address verification", connection.ID)
	}
	addresses, err := netif.ListAddrs(ctx, log, ready.ActualName)
	if err != nil {
		log.WarnContext(ctx, "addresses: installed address read failed", "connection_id", connection.ID, "err", err)
		return fmt.Errorf("verify installed addresses: %w", err)
	}
	verified := make([]string, 0, len(settings.Addresses))
	for _, desired := range settings.Addresses {
		found := false
		for _, current := range addresses {
			if current.CIDR == desired.Prefix.String() && current.Flags&unix.IFA_F_TENTATIVE == 0 {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("address %s is not installed and ready on %s", desired.Prefix, connection.Name)
		}
		verified = append(verified, desired.Prefix.String())
	}
	for _, prefix := range verified {
		module.Env.OwnedAddresses.Set(connection.ID.String(), prefix)
	}
	return nil
}

func mappedAddresses(connection interfaceintent.Connection, translation *config.IPv4Translation) []interfaceintent.Address {
	if translation == nil {
		return nil
	}
	var addresses []interfaceintent.Address
	for _, mapping := range translation.StaticMappings {
		if mapping.Delivery == interfaceintent.DeliveryRouted || staticAddressEquals(connection, mapping.External) {
			continue
		}
		if mapping.Delivery != interfaceintent.DeliveryLocal && !mappingOnLink(connection, mapping.External) {
			continue
		}
		addresses = append(addresses, interfaceintent.Address{Prefix: netip.PrefixFrom(mapping.External, 32), Purpose: interfaceintent.PurposeForward})
	}
	return addresses
}

func staticAddressEquals(connection interfaceintent.Connection, external netip.Addr) bool {
	if connection.IPv4 == nil {
		return false
	}
	for _, address := range connection.IPv4.Addresses {
		if address.Prefix.Addr() == external {
			return true
		}
	}
	return false
}

func mappingOnLink(connection interfaceintent.Connection, external netip.Addr) bool {
	if connection.IPv4 == nil {
		return false
	}
	for _, address := range connection.IPv4.Addresses {
		if address.Prefix.Bits() < 32 && address.Prefix.Masked().Contains(external) {
			return true
		}
	}
	return false
}

func nptAddress(translation *config.IPv6Translation) netip.Prefix {
	if translation == nil || translation.NPT == nil || translation.Mode != config.TranslationNPTv6 || translation.NPT.ExternalSource != config.PrefixConfigured {
		return netip.Prefix{}
	}
	prefix := translation.NPT.ExternalPrefix.Masked()
	if !prefix.IsValid() {
		return netip.Prefix{}
	}
	address := prefix.Addr().As16()
	address[15] = 1
	return netip.PrefixFrom(netip.AddrFrom16(address), 128)
}

func addressAssignment(connection interfaceintent.Connection, family string, kind interfaceintent.AssignmentKind, purpose interfaceintent.AddressPurpose, value netip.Prefix) interfaceintent.Assignment {
	return interfaceintent.Assignment{
		ConnectionID: connection.ID, Family: family, Kind: kind,
		Source: "configured", Purpose: purpose, Value: value, Route: nil, ClientID: "", DUID: "", IAID: nil,
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
