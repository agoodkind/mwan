package networkjson

import (
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/gateway/internal/config"
)

var (
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

	if err := checkBGPSessionSet(loaded, providerIDs); err != nil {
		return nil, err
	}

	return finishNetwork(doc, loaded, nil)
}
