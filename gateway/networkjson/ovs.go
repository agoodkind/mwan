package networkjson

// This builder combines OVS bridge and OpenFlow settings with the WAN
// connections that supply BGP sessions, probe verdicts, and prefix mappings.
// A conditional rule can depend on a probe for a different WAN connection.

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

var (
	errUnknownOVSWAN   = errors.New("unknown WAN connection")
	errConditionProbe  = errors.New("health condition requires an enabled WAN health probe")
	errConditionChoice = errors.New(
		"select exactly one healthy-connection or unhealthy-connection",
	)
	errIndirectCondition = errors.New(
		"an indirect group requires an unconditional bucket. Apply health conditions to referring flows",
	)
	errIndirectLeaseAction = errors.New(
		"an indirect group bucket cannot reference DHCP leases. Put lease-dependent actions on referring flows",
	)
	errOVSBackupExport = errors.New(
		"BGP backup-for requires kernel ISP forwarding readiness",
	)
	errMissingOVSProbeTimeout = errors.New(
		"steering-group/health/probe-timeout is required for enabled health probes",
	)
)

func buildOVSHost(doc *document, loaded *Config) (*Config, error) {
	if len(loaded.Rejected) != 0 {
		return nil, loaded.Rejected[0].Err
	}

	if err := validateActiveDependencies(loaded.Connections); err != nil {
		return nil, err
	}

	group := doc.Interfaces.SteeringGroup
	if group.Health.ProbeTimeout != nil {
		loaded.ProbeTimeoutMillis = *group.Health.ProbeTimeout
	}

	var providerIDs []string

	for _, entry := range doc.Interfaces.Interface {
		if entry.WAN == nil {
			continue
		}

		connectionID := loaded.ConnectionIDs[entry.Name].String()
		label := "wan " + entry.WAN.Name
		sessions, err := buildBGPSessions(label, entry)
		if err != nil {
			return nil, err
		}

		if err := loadOVSMapping(loaded.OVS.OpenFlow, connectionID, entry); err != nil {
			return nil, err
		}

		var provider config.IfMgrWANEntry

		for _, session := range sessions {
			if len(session.BackupFor) != 0 {
				slog.Error(
					"OVS gateway rejected a BGP backup export",
					"session",
					session.Session.Name,
					"err",
					errOVSBackupExport,
				)

				return nil, fmt.Errorf(
					"%s session %s: %w",
					label,
					session.Session.Name,
					errOVSBackupExport,
				)
			}
		}

		provider.ProviderName = entry.WAN.Name
		provider.Iface = entry.Name
		provider.LinkFiles = entry.LinkFiles
		provider.BGPSessions = sessions
		loaded.WAN[connectionID] = provider
		providerIDs = append(providerIDs, connectionID)
		if entry.WAN.Health == nil {
			continue
		}

		probe, err := buildHealth(label, entry.WAN.Health)
		if err != nil {
			return nil, err
		}

		if probe.Enabled && loaded.ProbeTimeoutMillis == 0 {
			return nil, errMissingOVSProbeTimeout
		}

		loaded.Health[connectionID] = *probe
	}

	if err := validateOVSConditions(loaded); err != nil {
		return nil, err
	}

	if err := checkBGPSessionSet(loaded, providerIDs); err != nil {
		return nil, err
	}

	return finishNetwork(doc, loaded, nil)
}

func loadOVSMapping(
	settings *ovsconfig.OpenFlowConfig,
	connectionID string,
	entry ifaceEntry,
) error {
	if settings == nil || entry.IPv6 == nil || entry.IPv6.Translation == nil ||
		entry.IPv6.Translation.Mode != config.TranslationNPTv6 {
		return nil
	}

	wire := entry.IPv6.Translation.NPT
	if wire != nil && wire.ExternalSource == "" && wire.ExternalPrefix != "" {
		wire.ExternalSource = config.PrefixConfigured
	}

	policy, err := buildTranslationV6("wan "+connectionID, entry)
	if err != nil {
		return err
	}

	if settings.Mappings == nil {
		settings.Mappings = make(map[string]config.NPTv6Translation)
	}

	settings.Mappings[connectionID] = *policy.NPT

	return nil
}

func checkOVSConnection(loaded *Config, connectionID string, condition *ovsconfig.Condition) error {
	if connectionID != "" {
		if _, ok := loaded.WAN[connectionID]; !ok {
			slog.Error(
				"OpenFlow entry selects an unknown WAN connection",
				"connection",
				connectionID,
				"err",
				errUnknownOVSWAN,
			)

			return fmt.Errorf("%w %q", errUnknownOVSWAN, connectionID)
		}
	}

	return checkOVSCondition(loaded, condition)
}

func validateOVSConditions(loaded *Config) error {
	settings := loaded.OVS.OpenFlow
	if settings == nil {
		return nil
	}

	for _, flow := range settings.Flows {
		if err := checkOVSConnection(loaded, flow.Connection, flow.Condition); err != nil {
			return fmt.Errorf("OpenFlow flow %q: %w", flow.Name, err)
		}
	}

	for _, group := range settings.Groups {
		for _, bucket := range group.Buckets {
			if group.Type == ovsconfig.GroupIndirect && bucket.Condition != nil {
				slog.Error(
					"Invalid OpenFlow bucket condition",
					"group",
					group.ID,
					"bucket",
					bucket.ID,
					"err",
					errIndirectCondition,
				)

				return fmt.Errorf(
					"OpenFlow group %d bucket %d: %w",
					group.ID,
					bucket.ID,
					errIndirectCondition,
				)
			}

			if err := checkOVSConnection(loaded, bucket.Connection, bucket.Condition); err != nil {
				return fmt.Errorf("OpenFlow group %d bucket %d: %w", group.ID, bucket.ID, err)
			}

			if group.Type == ovsconfig.GroupIndirect && slices.ContainsFunc(
				bucket.Actions,
				func(action ovsconfig.Action) bool {
					return strings.Contains(action.Argument, "${dhcpv4:") ||
						strings.Contains(action.Argument, "${dhcpv6-pd:")
				},
			) {
				slog.Error(
					"Invalid OpenFlow bucket action",
					"group",
					group.ID,
					"bucket",
					bucket.ID,
					"err",
					errIndirectLeaseAction,
				)

				return fmt.Errorf(
					"OpenFlow group %d bucket %d: %w",
					group.ID,
					bucket.ID,
					errIndirectLeaseAction,
				)
			}
		}
	}

	return nil
}

func checkOVSCondition(loaded *Config, condition *ovsconfig.Condition) error {
	if condition == nil {
		return nil
	}

	if (condition.HealthyConnection == "") == (condition.UnhealthyConnection == "") {
		return errConditionChoice
	}

	if !loaded.Health[condition.ConnectionID()].Enabled {
		slog.Error(
			"OVS condition requires a WAN probe",
			"connection_id",
			condition.ConnectionID(),
			"err",
			errConditionProbe,
		)

		return fmt.Errorf("%w: %q", errConditionProbe, condition.ConnectionID())
	}

	return nil
}
