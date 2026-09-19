package health

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/statuspush"
)

const (
	defaultStateFile         = "/var/run/mwan-health.state"
	defaultPersistStateFile  = "/var/lib/mwan/health-state"
	defaultTimeout           = 2 * time.Second
	defaultInterval          = 10 * time.Second
	defaultPingCount         = 3
	defaultSuccessThreshold  = 2
	defaultFailureThreshold  = 2
	defaultRecoveryThreshold = 2
)

func defaultTargetsV4() []netip.Addr {
	return []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("8.8.8.8"),
	}
}

func defaultTargetsV6() []netip.Addr {
	return []netip.Addr{
		netip.MustParseAddr("2606:4700:4700::1111"),
		netip.MustParseAddr("2001:4860:4860::8888"),
	}
}

func validateConfig(cfg Config) error {
	var validationError error
	validationError = errors.Join(validationError, validateProbeConfig(cfg))
	validationError = errors.Join(validationError, validateWANs(cfg))
	return validationError
}

func validateProbeConfig(cfg Config) error {
	var validationError error
	if cfg.StateFile == "" {
		validationError = errors.Join(validationError, errors.New("state_file is required"))
	}
	if cfg.PersistStateFile == "" {
		validationError = errors.Join(
			validationError,
			errors.New("persist_state_file is required"),
		)
	}
	if len(cfg.TargetsV6) == 0 {
		validationError = errors.Join(
			validationError,
			errors.New("at least one targets_v6 entry is required"),
		)
	}
	if len(cfg.TargetsV4) == 0 {
		validationError = errors.Join(
			validationError,
			errors.New("at least one targets_v4 entry is required"),
		)
	}
	if cfg.Timeout <= 0 {
		validationError = errors.Join(validationError, errors.New("timeout must be > 0"))
	}
	if cfg.Interval <= 0 {
		validationError = errors.Join(validationError, errors.New("interval must be > 0"))
	}
	if cfg.PingCount <= 0 {
		validationError = errors.Join(validationError, errors.New("ping_count must be > 0"))
	}
	if cfg.SuccessThreshold <= 0 {
		validationError = errors.Join(
			validationError,
			errors.New("success_threshold must be > 0"),
		)
	}
	if cfg.SuccessThreshold > len(cfg.TargetsV6) ||
		cfg.SuccessThreshold > len(cfg.TargetsV4) {
		validationError = errors.Join(
			validationError,
			errors.New("success_threshold exceeds an address-family target count"),
		)
	}
	if cfg.FailureThreshold <= 0 {
		validationError = errors.Join(
			validationError,
			errors.New("failure_threshold must be > 0"),
		)
	}
	if cfg.RecoveryThreshold <= 0 {
		validationError = errors.Join(
			validationError,
			errors.New("recovery_threshold must be > 0"),
		)
	}
	return validationError
}

func validateWANs(cfg Config) error {
	var validationError error
	seenNames := make(map[string]bool, len(cfg.WANs))
	seenIfaces := make(map[string]bool, len(cfg.WANs))
	for i, wan := range cfg.WANs {
		wanLabel := fmt.Sprintf("wan[%d] (%s)", i, wan.Name)
		if wan.Name == "" {
			validationError = errors.Join(
				validationError,
				fmt.Errorf("wan[%d]: name is required", i),
			)
		}
		if wan.Iface == "" {
			validationError = errors.Join(
				validationError,
				fmt.Errorf("wan[%d] (%s): iface is required", i, wan.Name),
			)
		}
		if seenNames[wan.Name] {
			validationError = errors.Join(
				validationError,
				fmt.Errorf("wan[%d]: duplicate name %q", i, wan.Name),
			)
		}
		if seenIfaces[wan.Iface] {
			validationError = errors.Join(
				validationError,
				fmt.Errorf("wan[%d]: duplicate iface %q", i, wan.Iface),
			)
		}
		successThreshold := wan.successThreshold(cfg)
		if successThreshold <= 0 {
			validationError = errors.Join(
				validationError,
				fmt.Errorf("%s: success_threshold must be > 0", wanLabel),
			)
		}
		// A provider must carry at least one ping target, but not one in each
		// family: a provider on an IPv4-only link declares an empty IPv6 list
		// and is judged on IPv4 alone. The threshold is checked against each
		// family the provider does carry, and says nothing about one it does not.
		targetsV6 := wan.targetsV6(cfg)
		targetsV4 := wan.targetsV4(cfg)
		if len(targetsV6) == 0 && len(targetsV4) == 0 {
			validationError = errors.Join(
				validationError,
				fmt.Errorf(
					"%s: at least one targets_v4 or targets_v6 entry is required",
					wanLabel,
				),
			)
		}
		if (len(targetsV6) > 0 && successThreshold > len(targetsV6)) ||
			(len(targetsV4) > 0 && successThreshold > len(targetsV4)) {
			validationError = errors.Join(
				validationError,
				fmt.Errorf(
					"%s: success_threshold exceeds an address-family target count",
					wanLabel,
				),
			)
		}
		if wan.pingCount(cfg) <= 0 {
			validationError = errors.Join(
				validationError,
				fmt.Errorf("%s: ping_count must be > 0", wanLabel),
			)
		}
		if wan.failureThreshold(cfg) <= 0 {
			validationError = errors.Join(
				validationError,
				fmt.Errorf("%s: failure_threshold must be > 0", wanLabel),
			)
		}
		if wan.recoveryThreshold(cfg) <= 0 {
			validationError = errors.Join(
				validationError,
				fmt.Errorf("%s: recovery_threshold must be > 0", wanLabel),
			)
		}
		seenNames[wan.Name] = true
		seenIfaces[wan.Iface] = true
	}
	return validationError
}

// New applies shell-compatible defaults before constructing the registered
// module so omitted TOML fields still produce the same baseline probe cadence.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	healthConfig := Config{
		StateFile:         "",
		PersistStateFile:  "",
		TargetsV4:         nil,
		TargetsV6:         nil,
		HTTPURLs:          nil,
		Timeout:           0,
		Interval:          0,
		PingCount:         0,
		SuccessThreshold:  0,
		FailureThreshold:  0,
		RecoveryThreshold: 0,
		StatusPushCID:     0,
		StatusPushPort:    0,
		WANs:              nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("health: invalid config type %T", cfg)
		}
		healthConfig = typedConfig
	}
	applyDefaults(&healthConfig)
	// A zero port means no watchdog is listening for this host's verdict, which
	// is every host but the two gateways. Building no sender there keeps a
	// pointless dial out of every probe cycle.
	var pusher statusSender
	if healthConfig.StatusPushPort != 0 {
		pusher = statuspush.NewSender(
			healthConfig.StatusPushCID,
			healthConfig.StatusPushPort,
			slog.Default().With("component", "ifmgr", "module", moduleName),
		)
	}
	return &Module{
		BaseModule:       ifmgr.NewBaseModule(moduleName),
		cfg:              healthConfig,
		clock:            nil,
		cycleMu:          sync.Mutex{},
		reconcileMu:      sync.Mutex{},
		reconcilePending: true,
		statuses:         nil,
		lastTransition:   nil,
		probeV4:          netif.Ping4,
		probeV6:          netif.Ping6,
		probeHTTP6:       netif.HTTPCheck6,
		probeHTTP4:       netif.HTTPCheck4,
		pusher:           pusher,
	}, nil
}

func applyDefaults(cfg *Config) {
	if cfg.StateFile == "" {
		cfg.StateFile = defaultStateFile
	}
	if cfg.PersistStateFile == "" {
		cfg.PersistStateFile = defaultPersistStateFile
	}
	if len(cfg.TargetsV4) == 0 {
		cfg.TargetsV4 = defaultTargetsV4()
	}
	if len(cfg.TargetsV6) == 0 {
		cfg.TargetsV6 = defaultTargetsV6()
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}
	if cfg.Interval == 0 {
		cfg.Interval = defaultInterval
	}
	if cfg.PingCount == 0 {
		cfg.PingCount = defaultPingCount
	}
	if cfg.SuccessThreshold == 0 {
		cfg.SuccessThreshold = defaultSuccessThreshold
	}
	if cfg.FailureThreshold == 0 {
		cfg.FailureThreshold = defaultFailureThreshold
	}
	if cfg.RecoveryThreshold == 0 {
		cfg.RecoveryThreshold = defaultRecoveryThreshold
	}
}

func init() { ifmgr.Register(moduleName, New) }
