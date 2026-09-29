// Package autoconfiguration applies IPv6 router discovery policy to MWAN-owned links.
package autoconfiguration

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
)

const moduleName = "autoconfiguration"

// Config supplies MWAN connection policies to the autoconfiguration module.
type Config struct {
	Connections []interfaceintent.Connection
}

// ModuleConfigName identifies the autoconfiguration module.
func (Config) ModuleConfigName() string { return moduleName }

// Module applies kernel router discovery policy to owned links.
type Module struct {
	ifmgr.BaseModule
	connections []interfaceintent.Connection
}

// New validates the connection policies before the module starts.
func New(config ifmgr.ModuleConfig) (ifmgr.Module, error) {
	module := &Module{BaseModule: ifmgr.NewBaseModule(moduleName), connections: nil}
	if config == nil {
		return module, nil
	}
	settings, ok := config.(Config)
	if !ok {
		return nil, fmt.Errorf("autoconfiguration: invalid config type %T", config)
	}
	for _, connection := range settings.Connections {
		if connection.Owner != interfaceintent.OwnerMWAN || connection.IPv6 == nil {
			continue
		}
		if err := validateIPv6(connection.Name, *connection.IPv6); err != nil {
			return nil, err
		}
		module.connections = append(module.connections, connection)
	}
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
	if len(module.connections) == 0 {
		return ifmgr.ErrModuleDisabled
	}
	if env.OwnedLinks == nil || env.Sysctl == nil {
		return fmt.Errorf("autoconfiguration: owned links and sysctl access are required")
	}
	return nil
}

// Reconcile applies configured policy to ready owned links.
func (module *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	for _, connection := range module.connections {
		ready, ok := module.Env.OwnedLinks.Get(connection.ID.String())
		if !ok || ready.Status != netif.OwnedLinkReady || ready.ActualName == "" {
			continue
		}
		if err := module.apply(ctx, ready.ActualName, *connection.IPv6); err != nil {
			log.WarnContext(ctx, "autoconfiguration: apply failed", "connection_id", connection.ID, "interface", ready.ActualName, "err", err)
			return fmt.Errorf("autoconfiguration: interface %s: %w", connection.Name, err)
		}
	}
	return nil
}

type setting struct {
	key  string
	want string
	have string
}

func (module *Module) apply(ctx context.Context, name string, ipv6 interfaceintent.IPv6) error {
	base := "net.ipv6.conf." + name + "."
	settings := make([]setting, 0, 5)
	if ipv6.AcceptRA != nil && !*ipv6.AcceptRA {
		settings = append(settings, setting{key: base + "accept_ra", want: "0", have: ""})
	}
	if ipv6.AutoConf != nil || ipv6.AcceptRA != nil {
		autoconf := ipv6.AcceptRA != nil && *ipv6.AcceptRA
		if ipv6.AutoConf != nil {
			autoconf = *ipv6.AutoConf
		}
		settings = append(settings, setting{key: base + "autoconf", want: sysctlBool(autoconf), have: ""})
	}
	if ipv6.AcceptRADefaultRoute != nil || ipv6.AcceptRA != nil {
		defaultRoute := ipv6.AcceptRA != nil && *ipv6.AcceptRA
		if ipv6.AcceptRADefaultRoute != nil {
			defaultRoute = *ipv6.AcceptRADefaultRoute
		}
		settings = append(settings, setting{key: base + "accept_ra_defrtr", want: sysctlBool(defaultRoute), have: ""})
	}
	if ipv6.RouteMetric != nil && !ipv6.Gateway.IsValid() {
		settings = append(settings, setting{key: base + "ra_defrtr_metric", want: strconv.FormatUint(uint64(*ipv6.RouteMetric), 10), have: ""})
	}
	if ipv6.Forwarding != nil {
		settings = append(settings, setting{key: base + "forwarding", want: sysctlBool(*ipv6.Forwarding), have: ""})
	}
	if ipv6.AcceptRA != nil && *ipv6.AcceptRA {
		settings = append(settings, setting{key: base + "accept_ra", want: "2", have: ""})
	}
	for i := range settings {
		current, err := module.Env.Sysctl.Get(ctx, settings[i].key)
		if err != nil {
			slog.WarnContext(ctx, "autoconfiguration: sysctl read failed", "key", settings[i].key, "err", err)
			return fmt.Errorf("read %s: %w", settings[i].key, err)
		}
		settings[i].have = current
	}
	for _, value := range settings {
		if value.have == value.want {
			continue
		}
		if err := module.Env.Sysctl.Set(ctx, value.key, value.want); err != nil {
			slog.WarnContext(ctx, "autoconfiguration: sysctl write failed", "key", value.key, "err", err)
			return fmt.Errorf("set %s=%s: %w", value.key, value.want, err)
		}
	}
	return nil
}

func sysctlBool(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func init() { ifmgr.Register(moduleName, New) }
