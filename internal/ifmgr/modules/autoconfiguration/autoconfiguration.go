// Package autoconfiguration applies per-link forwarding and IPv6 router discovery policy.
package autoconfiguration

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

const moduleName = "autoconfiguration"

// Config supplies MWAN connection policies to the autoconfiguration module.
type Config struct {
	Connections []interfaceintent.Connection
	StateFile   string
}

// ModuleConfigName identifies the autoconfiguration module.
func (Config) ModuleConfigName() string { return moduleName }

// Module applies kernel router discovery policy to owned links.
type Module struct {
	ifmgr.BaseModule
	connections []interfaceintent.Connection
	stateFile   string
	reconciler  *netif.OwnedKernelPolicyReconciler
}

// New validates the connection policies before the module starts.
func New(config ifmgr.ModuleConfig) (ifmgr.Module, error) {
	module := &Module{BaseModule: ifmgr.NewBaseModule(moduleName), connections: nil, stateFile: "", reconciler: nil}
	if config == nil {
		return module, nil
	}
	settings, ok := config.(Config)
	if !ok {
		return nil, fmt.Errorf("autoconfiguration: invalid config type %T", config)
	}
	for _, connection := range settings.Connections {
		if connection.Owner != interfaceintent.OwnerMWAN {
			continue
		}
		if connection.IPv6 != nil {
			if err := validateIPv6(connection.Name, *connection.IPv6); err != nil {
				return nil, err
			}
		}
		module.connections = append(module.connections, connection)
	}
	module.stateFile = settings.StateFile
	return module, nil
}

func validateIPv6(name string, ipv6 interfaceintent.IPv6) error {
	if ipv6.UseRADNS != nil && *ipv6.UseRADNS {
		return fmt.Errorf("autoconfiguration: interface %s requires an RA DNS resolver integration", name)
	}
	if ipv6.AcceptRA == nil || !*ipv6.AcceptRA {
		if ipv6.AutoConf != nil && *ipv6.AutoConf ||
			ipv6.AcceptRADefaultRoute != nil && *ipv6.AcceptRADefaultRoute {
			return fmt.Errorf("autoconfiguration: interface %s enables an RA option without accept-ra", name)
		}
	}
	if ipv6.RouteMetric != nil && !ipv6.Gateway.IsValid() {
		if ipv6.AcceptRA == nil || !*ipv6.AcceptRA ||
			ipv6.AcceptRADefaultRoute != nil && !*ipv6.AcceptRADefaultRoute ||
			*ipv6.RouteMetric == 0 {
			return fmt.Errorf("autoconfiguration: interface %s has invalid RA default route metric", name)
		}
	}
	return nil
}

// Init receives the owned-link state and sysctl writer.
func (module *Module) Init(_ context.Context, env *ifmgr.Env) error {
	module.InitBase(env, "module", moduleName)
	if len(module.connections) == 0 && module.stateFile == "" {
		return ifmgr.ErrModuleDisabled
	}
	if env.OwnedLinks == nil || env.Sysctl == nil {
		return fmt.Errorf("autoconfiguration: owned links and sysctl access are required")
	}
	reconciler, err := netif.NewOwnedKernelPolicyReconciler(module.stateFile, env.Sysctl)
	if err != nil {
		slog.Warn("autoconfiguration: kernel policy initialization failed", "err", err)
		return fmt.Errorf("initialize autoconfiguration kernel policy: %w", err)
	}
	module.reconciler = reconciler
	return nil
}

// Reconcile applies configured policy to ready owned links.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	results := make([]netif.OwnedLinkResult, 0, len(module.connections))
	for _, connection := range module.connections {
		if result, found := module.Env.OwnedLinks.Get(connection.ID.String()); found {
			results = append(results, result)
		}
	}
	if err := module.reconciler.Reconcile(ctx, module.connections, results); err != nil {
		log.WarnContext(ctx, "autoconfiguration: kernel policy reconciliation failed", "err", err)
		return fmt.Errorf("reconcile autoconfiguration kernel policy: %w", err)
	}
	return nil
}

func init() { ifmgr.Register(moduleName, New) }
