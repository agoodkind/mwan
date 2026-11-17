package moduleconfig

import (
	"fmt"
	"log/slog"
	"net/netip"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/ifmgr/modules/health"
	"goodkind.io/mwan/internal/observation"
)

// HealthConfig adds daemon observation and state settings to the probe policy.
//
// It reads the observation settings file when the health section specifies one.
//
//	cfg, err := moduleconfig.HealthConfig(shared, settings.Modules.Health)
func HealthConfig(
	shared WANInputs,
	section *config.IfMgrHealthSection,
) (health.Config, error) {
	var observations *observation.ContinuousSettings
	if section != nil && section.ObservationSettingsFile != "" {
		settings, err := observation.LoadContinuousSettings(section.ObservationSettingsFile)
		if err != nil {
			slog.Warn(
				"health observation settings rejected",
				"path",
				section.ObservationSettingsFile,
				"err",
				err,
			)

			return health.Config{}, fmt.Errorf("load health observations: %w", err)
		}

		observations = &settings
	}

	cfg, err := HealthProviderConfig(shared, section)
	if err != nil {
		slog.Warn("health provider policy rejected", "err", err)

		return health.Config{}, err
	}

	if section != nil {
		cfg.Observations = observations
		cfg.StateFile = section.StateFile
		cfg.StatusPushCID = section.StatusPushCID
		cfg.StatusPushPort = section.StatusPushPort
	}

	return cfg, nil
}

// HealthProviderConfig builds the probe policy for the configured WANs.
//
// Operator probes use this policy without reading daemon observation files.
//
//	cfg, err := moduleconfig.HealthProviderConfig(shared, settings.Modules.Health)
func HealthProviderConfig(
	shared WANInputs,
	section *config.IfMgrHealthSection,
) (health.Config, error) {
	cfg := health.Config{
		Observations:      nil,
		StateFile:         "",
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
		WANs:              make([]health.WAN, 0, len(shared.WANs)),
	}
	if section == nil {
		for _, wan := range shared.WANs {
			cfg.WANs = append(
				cfg.WANs,
				health.WAN{
					WANRef:            wan.WANRef,
					Tier:              wan.Tier,
					TargetsV4:         tunnelTargetsV4(wan, nil),
					TargetsV6:         nil,
					HTTPURLs:          nil,
					PingCount:         0,
					SuccessThreshold:  0,
					FailureThreshold:  0,
					RecoveryThreshold: 0,
					CheckInterval:     0,
				},
			)
		}

		return cfg, nil
	}

	cfg.Timeout = time.Duration(section.ProbeTimeoutMillis) * time.Millisecond

	var err error

	for _, wan := range shared.WANs {
		wanSection, ok := section.WAN[wan.Key()]
		if !ok || !wanSection.Enabled {
			continue
		}

		// Each enabled provider supplies its own thresholds. A zero threshold
		// would make the health module use its default for that provider.
		if err := validateHealthWANSection(wan.Name, wanSection); err != nil {
			return health.Config{}, err
		}

		healthWAN := health.WAN{
			WANRef:            wan.WANRef,
			Tier:              wan.Tier,
			TargetsV4:         nil,
			TargetsV6:         nil,
			HTTPURLs:          append([]string(nil), wanSection.HTTPURLs...),
			PingCount:         healthSettingValue(wanSection.PingCount),
			SuccessThreshold:  healthSettingValue(wanSection.SuccessThreshold),
			FailureThreshold:  healthSettingValue(wanSection.FailureThreshold),
			RecoveryThreshold: healthSettingValue(wanSection.RecoveryThreshold),
			CheckInterval:     0,
		}
		fieldPrefix := "network.json wan " + wan.Name + " health"
		healthWAN.TargetsV4, err = parseHealthTargets(
			wanSection.TargetsV4,
			fieldPrefix+"/targets-v4",
		)
		if err != nil {
			return health.Config{}, err
		}

		healthWAN.TargetsV4 = tunnelTargetsV4(wan, healthWAN.TargetsV4)
		healthWAN.TargetsV6, err = parseHealthTargets(
			wanSection.TargetsV6,
			fieldPrefix+"/targets-v6",
		)
		if err != nil {
			return health.Config{}, err
		}

		healthWAN.CheckInterval = time.Duration(
			healthSettingValue(wanSection.CheckIntervalSeconds),
		) * time.Second
		if healthWAN.CheckInterval != 0 &&
			(cfg.Interval == 0 || healthWAN.CheckInterval < cfg.Interval) {
			cfg.Interval = healthWAN.CheckInterval
		}

		cfg.WANs = append(cfg.WANs, healthWAN)
	}

	return cfg, nil
}

func tunnelTargetsV4(wan sharedWAN, configured []netip.Addr) []netip.Addr {
	if wan.Tunnel != nil && configured == nil {
		return []netip.Addr{}
	}

	return configured
}

// A provider may probe either address family. Every family with targets
// must have enough targets to meet the success threshold.
func validateHealthWANSection(name string, section config.IfMgrHealthWANSection) error {
	const positive = "must be > 0"

	prefix := "network.json wan " + name + " health"
	successThreshold := healthSettingValue(section.SuccessThreshold)
	if healthSettingValue(section.PingCount) <= 0 {
		return healthPolicyError{field: prefix + "/ping-count", problem: positive}
	}

	if successThreshold <= 0 {
		return healthPolicyError{field: prefix + "/success-threshold", problem: positive}
	}

	if healthSettingValue(section.FailureThreshold) <= 0 {
		return healthPolicyError{field: prefix + "/failure-threshold", problem: positive}
	}

	if healthSettingValue(section.RecoveryThreshold) <= 0 {
		return healthPolicyError{field: prefix + "/recovery-threshold", problem: positive}
	}

	if len(section.TargetsV4) == 0 && len(section.TargetsV6) == 0 {
		return healthPolicyError{
			field:   prefix,
			problem: "must have at least one targets-v4 or targets-v6 entry",
		}
	}

	if len(section.TargetsV4) > 0 && successThreshold > len(section.TargetsV4) {
		return healthPolicyError{
			field: prefix + "/success-threshold",
			problem: fmt.Sprintf(
				"%d exceeds targets-v4 count %d",
				successThreshold,
				len(section.TargetsV4),
			),
		}
	}

	if len(section.TargetsV6) > 0 && successThreshold > len(section.TargetsV6) {
		return healthPolicyError{
			field: prefix + "/success-threshold",
			problem: fmt.Sprintf(
				"%d exceeds targets-v6 count %d",
				successThreshold,
				len(section.TargetsV6),
			),
		}
	}

	return nil
}

type healthPolicyError struct {
	field   string
	problem string
}

func (failure healthPolicyError) Error() string {
	return failure.field + " " + failure.problem
}

func healthSettingValue(setting *int) int {
	if setting == nil {
		return 0
	}

	return *setting
}

// Health probes distinguish omitted targets from an explicitly empty list.
func parseHealthTargets(values []string, fieldName string) ([]netip.Addr, error) {
	if values == nil {
		return nil, nil
	}

	addresses := make([]netip.Addr, 0, len(values))
	for index, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil {
			return nil, healthTargetError{field: fieldName, index: index, value: value, cause: err}
		}

		addresses = append(addresses, address)
	}

	return addresses, nil
}

type healthTargetError struct {
	field string
	index int
	value string
	cause error
}

func (failure healthTargetError) Error() string {
	return fmt.Sprintf("%s[%d] %q: %v", failure.field, failure.index, failure.value, failure.cause)
}

func (failure healthTargetError) Unwrap() error {
	return failure.cause
}
