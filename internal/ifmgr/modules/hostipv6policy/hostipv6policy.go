// Package hostipv6policy keeps the suburban hypervisor's host-side IPv6
// RA policy aligned with the intended bridge roles. Proxmox still owns
// bridge existence and shape; this module only reconciles live kernel
// sysctls and removes stale RA-learned defaults from denied interfaces.
package hostipv6policy

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/mdlayher/ndp"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

const (
	defaultMissingIfaceGracePeriod = 2 * time.Minute
	raSolicitTimeout               = 5 * time.Second
)

// InterfacePolicy is one host interface's desired IPv6 RA behaviour.
type InterfacePolicy struct {
	Name             string
	AcceptRA         int
	AutoConf         bool
	AcceptRADefRtr   bool
	SolicitRA        bool
	CleanupRADefault bool
}

// Config is the parsed [ifmgr.modules.host_ipv6_policy] sub-config.
type Config struct {
	MissingIfaceGracePeriod time.Duration
	Policies                []InterfacePolicy
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "host_ipv6_policy" }

type routerSoliciter interface {
	SolicitRA(ctx context.Context, timeout time.Duration) (*ndp.RouterAdvertisement, error)
	Close() error
}

// Module owns the host-side IPv6 RA policy.
type Module struct {
	ifmgr.BaseModule

	cfg Config

	missingSince map[string]time.Time

	now                  func() time.Time
	findMainRADefault    func(context.Context, string) (*netif.CurrentRoute, error)
	deleteMainRADefaults func(context.Context, *slog.Logger, string) (int, error)
	newRAClient          func(string, *slog.Logger) (routerSoliciter, error)
}

// Init implements ifmgr.Module. An empty Policies list means no
// [ifmgr.modules.host_ipv6_policy] section was rendered for this host,
// so Init returns ifmgr.ErrModuleDisabled and the daemon drops the
// module from its dispatch list.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "host_ipv6_policy")
	log.InfoContext(ctx, "host_ipv6_policy: Init",
		"policy_count", len(m.cfg.Policies),
		"missing_iface_grace_period", m.cfg.MissingIfaceGracePeriod.String(),
	)
	if len(m.cfg.Policies) == 0 {
		log.WarnContext(ctx, "host_ipv6_policy: module disabled because no policies were configured")
		return fmt.Errorf("%w: host_ipv6_policy: no [ifmgr.modules.host_ipv6_policy] section", ifmgr.ErrModuleDisabled)
	}
	if m.cfg.MissingIfaceGracePeriod <= 0 {
		return fmt.Errorf("host_ipv6_policy: missing_iface_grace_period must be > 0")
	}
	seenIfaces := make(map[string]bool, len(m.cfg.Policies))
	for i, policy := range m.cfg.Policies {
		if policy.Name == "" {
			return fmt.Errorf("host_ipv6_policy[%d]: name is required", i)
		}
		if policy.AcceptRA < 0 || policy.AcceptRA > 2 {
			return fmt.Errorf("host_ipv6_policy[%d]: accept_ra must be 0, 1, or 2", i)
		}
		if seenIfaces[policy.Name] {
			return fmt.Errorf("host_ipv6_policy[%d]: duplicate iface %q", i, policy.Name)
		}
		seenIfaces[policy.Name] = true
	}
	return nil
}

// Reconcile implements ifmgr.Module.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	var reconcileErr error
	for _, policy := range m.cfg.Policies {
		policyLog := log.With("policy_iface", policy.Name)
		if err := m.reconcilePolicy(ctx, policyLog, policy); err != nil {
			reconcileErr = errors.Join(reconcileErr, err)
		}
	}
	return reconcileErr
}

func (m *Module) reconcilePolicy(
	ctx context.Context,
	log *slog.Logger,
	policy InterfacePolicy,
) error {
	missingIface, err := m.reconcilePolicySysctls(ctx, log, policy)
	if err != nil {
		if missingIface {
			return m.handleMissingIface(log, policy.Name, err)
		}
		return err
	}
	m.clearMissingIface(policy.Name)

	if policy.CleanupRADefault {
		return m.cleanupDeniedRADefault(ctx, log, policy.Name)
	}
	if policy.SolicitRA {
		return m.solicitAllowedIfaceRA(ctx, log, policy.Name)
	}
	return nil
}

func (m *Module) reconcilePolicySysctls(
	ctx context.Context,
	log *slog.Logger,
	policy InterfacePolicy,
) (bool, error) {
	sysctls := []struct {
		key  string
		want string
	}{
		{key: acceptRAKey(policy.Name), want: strconv.Itoa(policy.AcceptRA)},
		{key: autoconfKey(policy.Name), want: boolToSysctl(policy.AutoConf)},
		{key: acceptRADefRtrKey(policy.Name), want: boolToSysctl(policy.AcceptRADefRtr)},
	}
	for _, sysctl := range sysctls {
		missingIface, err := m.reconcileSysctl(ctx, log, sysctl.key, sysctl.want)
		if err != nil {
			return missingIface, err
		}
	}
	return false, nil
}

func (m *Module) reconcileSysctl(
	ctx context.Context,
	log *slog.Logger,
	key string,
	want string,
) (bool, error) {
	currentValue, err := m.Env.Sysctl.Get(ctx, key)
	if err != nil {
		wrappedErr := fmt.Errorf("host_ipv6_policy: get %s: %w", key, err)
		if errors.Is(err, os.ErrNotExist) {
			log.WarnContext(ctx, "host_ipv6_policy: sysctl key unavailable", "key", key, "err", wrappedErr)
			return true, wrappedErr
		}
		log.WarnContext(ctx, "host_ipv6_policy: failed to read sysctl", "key", key, "err", wrappedErr)
		return false, wrappedErr
	}
	if currentValue == want {
		return false, nil
	}
	log.InfoContext(ctx, "host_ipv6_policy: updating sysctl",
		"key", key,
		"current", currentValue,
		"want", want,
	)
	if err := m.Env.Sysctl.Set(ctx, key, want); err != nil {
		wrappedErr := fmt.Errorf("host_ipv6_policy: set %s=%s: %w", key, want, err)
		if errors.Is(err, os.ErrNotExist) {
			log.WarnContext(ctx, "host_ipv6_policy: sysctl key disappeared during update", "key", key, "err", wrappedErr)
			return true, wrappedErr
		}
		log.WarnContext(ctx, "host_ipv6_policy: failed to update sysctl", "key", key, "want", want, "err", wrappedErr)
		return false, wrappedErr
	}
	return false, nil
}

func (m *Module) cleanupDeniedRADefault(
	ctx context.Context,
	log *slog.Logger,
	iface string,
) error {
	currentRoute, err := m.findMainRADefault(ctx, iface)
	if err != nil {
		return err
	}
	if currentRoute == nil {
		return nil
	}
	log.InfoContext(ctx, "host_ipv6_policy: removing denied RA default",
		"via", currentRoute.Via,
		"dev", currentRoute.Dev,
	)
	deletedCount, err := m.deleteMainRADefaults(ctx, log, iface)
	if err != nil {
		return err
	}
	if deletedCount > 0 {
		log.InfoContext(ctx, "host_ipv6_policy: removed denied RA defaults",
			"count", deletedCount,
		)
	}
	return nil
}

func (m *Module) solicitAllowedIfaceRA(
	ctx context.Context,
	log *slog.Logger,
	iface string,
) error {
	currentRoute, err := m.findMainRADefault(ctx, iface)
	if err != nil {
		return err
	}
	if currentRoute != nil {
		return nil
	}
	client, err := m.newRAClient(iface, log)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := client.Close(); closeErr != nil {
			log.DebugContext(ctx, "host_ipv6_policy: RA client close failed", "err", closeErr)
		}
	}()
	log.InfoContext(ctx, "host_ipv6_policy: soliciting RA on allowed iface")
	if _, err := client.SolicitRA(ctx, raSolicitTimeout); err != nil {
		log.WarnContext(ctx, "host_ipv6_policy: RA solicit did not yield a usable advertisement", "err", err)
		return nil
	}
	currentRoute, err = m.findMainRADefault(ctx, iface)
	if err != nil {
		return err
	}
	if currentRoute != nil {
		log.InfoContext(ctx, "host_ipv6_policy: RA default learned",
			"via", currentRoute.Via,
			"dev", currentRoute.Dev,
		)
	} else {
		log.WarnContext(ctx, "host_ipv6_policy: RA solicit completed without a main-table default")
	}
	return nil
}

func (m *Module) handleMissingIface(log *slog.Logger, iface string, cause error) error {
	now := m.now()
	m.Lock()
	firstSeenAt, exists := m.missingSince[iface]
	if !exists {
		firstSeenAt = now
		m.missingSince[iface] = now
	}
	m.Unlock()

	missingDuration := now.Sub(firstSeenAt)
	if missingDuration < m.cfg.MissingIfaceGracePeriod {
		log.Info("host_ipv6_policy: waiting for iface to appear",
			"iface", iface,
			"age", missingDuration.String(),
			"grace_period", m.cfg.MissingIfaceGracePeriod.String(),
		)
		return nil
	}
	log.Warn("host_ipv6_policy: iface still missing after grace period",
		"iface", iface,
		"age", missingDuration.Truncate(time.Second).String(),
		"grace_period", m.cfg.MissingIfaceGracePeriod.String(),
		"err", cause,
	)
	return fmt.Errorf(
		"host_ipv6_policy: iface %q still missing after %s: %w",
		iface,
		missingDuration.Truncate(time.Second).String(),
		cause,
	)
}

func (m *Module) clearMissingIface(iface string) {
	m.Lock()
	delete(m.missingSince, iface)
	m.Unlock()
}

// New is the Constructor.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	parsedConfig := Config{
		MissingIfaceGracePeriod: defaultMissingIfaceGracePeriod,
		Policies:                nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("host_ipv6_policy: invalid config type %T", cfg)
		}
		parsedConfig = typedConfig
		if parsedConfig.MissingIfaceGracePeriod == 0 {
			parsedConfig.MissingIfaceGracePeriod = defaultMissingIfaceGracePeriod
		}
	}
	return &Module{
		BaseModule:           ifmgr.NewBaseModule("host_ipv6_policy"),
		cfg:                  parsedConfig,
		missingSince:         make(map[string]time.Time),
		now:                  time.Now,
		findMainRADefault:    netif.FindMainRADefault,
		deleteMainRADefaults: netif.DeleteMainRADefaults,
		newRAClient: func(iface string, log *slog.Logger) (routerSoliciter, error) {
			return netif.NewRAClient(iface, log)
		},
	}, nil
}

func acceptRAKey(iface string) string {
	return fmt.Sprintf("net.ipv6.conf.%s.accept_ra", iface)
}

func autoconfKey(iface string) string {
	return fmt.Sprintf("net.ipv6.conf.%s.autoconf", iface)
}

func acceptRADefRtrKey(iface string) string {
	return fmt.Sprintf("net.ipv6.conf.%s.accept_ra_defrtr", iface)
}

func boolToSysctl(enabled bool) string {
	if enabled {
		return "1"
	}
	return "0"
}

func init() { ifmgr.Register("host_ipv6_policy", New) }
