package ovs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

const (
	secureMode     = "secure"
	statusInterval = 50 * time.Millisecond
)

var (
	errSwitchRow = errors.New("initialize OVS with one Open_vSwitch row")
	errDatapath  = errors.New(
		"check that ovs-vswitchd is running and supports this datapath",
	)
	errDOCASettings   = errors.New("OVS-DOCA setting is required")
	errBridgeOwner    = errors.New("existing OVS bridge lacks mwan ownership")
	errBridgeSettings = errors.New(
		"match datapath-type, use secure fail mode, and leave controllers unset",
	)
	errPortOwner         = errors.New("OVS port must have one mwan managed interface")
	errInterfaceRejected = errors.New("OVS rejected interface options")
	errMissingPort       = errors.New("OVS did not assign an OpenFlow port")
	errInterfaceConfig   = errors.New("OVS interface differs from its configuration")
)

// Setup configures an OVS bridge, its ports, and its OpenFlow rules.
//
// It installs rules only after ovs-vswitchd assigns OpenFlow port numbers.
//
// Example:
//
//	ovs.Setup(ctx, config)
func Setup(parent context.Context, config ovsconfig.Config) error {
	ctx, cancel := context.WithTimeout(parent, time.Duration(config.TimeoutMillis)*time.Millisecond)
	defer cancel()
	database, err := connect(ctx, config.Socket)
	if err != nil {
		return err
	}
	defer database.Close()

	return reconcileBridge(ctx, database, config)
}

func reconcileBridge(ctx context.Context, database databaseClient, config ovsconfig.Config) error {
	root, err := readSwitch(ctx, database, config)
	if err != nil {
		return err
	}

	var bridge bridgeRow
	bridge.Name = config.Bridge
	lookupErr := database.Get(ctx, &bridge)
	if lookupErr != nil && !errors.Is(lookupErr, client.ErrNotFound) {
		slog.ErrorContext(
			ctx,
			"OVS bridge lookup failed",
			"bridge",
			config.Bridge,
			"err",
			lookupErr,
		)

		return fmt.Errorf("read OVS bridge %q: %w", config.Bridge, lookupErr)
	}

	ports := append(
		[]ovsconfig.Port{{Name: config.Bridge, Type: "internal", Options: nil}},
		config.Ports...,
	)
	matches, err := bridgeMatches(ctx, database, bridge, config, ports)
	if err != nil || matches {
		return err
	}

	operations, err := configurationChanges(
		ctx,
		database,
		&root,
		bridge,
		config.DatapathType,
		ports,
	)
	if err != nil {
		return err
	}

	if err := transact(ctx, database, operations); err != nil {
		return err
	}

	return waitForPorts(ctx, database, root, ports)
}

func bridgeMatches(
	ctx context.Context,
	database databaseClient,
	bridge bridgeRow,
	config ovsconfig.Config,
	ports []ovsconfig.Port,
) (bool, error) {
	if bridge.UUID == "" {
		return false, nil
	}

	current, err := ownedPorts(ctx, database, bridge, config.DatapathType)
	if err != nil || len(current) != len(ports) {
		return false, err
	}

	for _, port := range ports {
		if _, exists := current[port.Name]; !exists {
			return false, nil
		}
	}

	return checkPorts(ctx, database, ports) == nil, nil
}

func readSwitch(
	ctx context.Context,
	database databaseClient,
	config ovsconfig.Config,
) (switchRow, error) {
	var rows []switchRow
	if err := database.List(ctx, &rows); err != nil {
		slog.ErrorContext(
			ctx,
			"OVS setup operation failed",
			"operation",
			"read Open_vSwitch row",
			"err",
			err,
		)

		return switchRow{}, fmt.Errorf("read Open_vSwitch row: %w", err)
	}

	if len(rows) != 1 {
		return switchRow{}, fmt.Errorf("%w; found %d rows", errSwitchRow, len(rows))
	}

	root := rows[0]
	if !slices.Contains(root.DatapathTypes, config.DatapathType) {
		return switchRow{}, fmt.Errorf("%w: %q", errDatapath, config.DatapathType)
	}

	if config.Offload == "doca" {
		for _, key := range []string{"doca-init", "hw-offload"} {
			if root.OtherConfig[key] != "true" {
				return switchRow{}, fmt.Errorf(
					"%w: Open_vSwitch.other_config:%s=true",
					errDOCASettings,
					key,
				)
			}
		}
	}

	return root, nil
}

func configurationChanges(
	ctx context.Context,
	database databaseClient,
	root *switchRow,
	bridge bridgeRow,
	datapath string,
	ports []ovsconfig.Port,
) ([]ovsdb.Operation, error) {
	now := 0
	switchQuery := database.Where(root)
	operations, err := switchQuery.Wait(
		ovsdb.WaitConditionEqual,
		&now,
		root,
		&root.NextConfig,
		&root.Bridges,
	)
	if err != nil {
		slog.ErrorContext(ctx, "OVSDB revision check failed", "err", err)

		return nil, fmt.Errorf("check OVSDB configuration revision: %w", err)
	}

	current, err := ownedPorts(ctx, database, bridge, datapath)
	if err != nil {
		return nil, err
	}

	bridge.Ports = make([]string, 0, len(ports))
	for index, port := range ports {
		portID, changes, err := portChanges(ctx, database, current[port.Name], port, index)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"OVS setup operation failed",
				"operation",
				"prepare OVS port",
				"err",
				err,
			)

			return nil, fmt.Errorf("prepare OVS port %q: %w", port.Name, err)
		}

		bridge.Ports = append(bridge.Ports, portID)
		operations = append(operations, changes...)
	}

	changes, err := bridgeChanges(ctx, database, root, bridge, datapath)
	if err != nil {
		return nil, err
	}

	operations = append(operations, changes...)

	root.NextConfig++
	changes, err = switchQuery.Update(root, &root.NextConfig, &root.Bridges)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"OVS setup operation failed",
			"operation",
			"prepare OVSDB configuration revision",
			"err",
			err,
		)

		return nil, fmt.Errorf("prepare OVSDB configuration revision: %w", err)
	}

	return append(operations, changes...), nil
}

func bridgeChanges(
	ctx context.Context,
	database databaseClient,
	root *switchRow,
	bridge bridgeRow,
	datapath string,
) ([]ovsdb.Operation, error) {
	if bridge.UUID != "" {
		changes, err := database.Where(&bridge).Update(&bridge, &bridge.Ports, &bridge.Protocols)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"OVS bridge port update preparation failed",
				"bridge",
				bridge.Name,
				"err",
				err,
			)

			return nil, fmt.Errorf("update OVS bridge %q ports: %w", bridge.Name, err)
		}

		return changes, nil
	}

	bridge.UUID = "mwan_bridge"
	bridge.DatapathType = datapath
	mode := secureMode
	bridge.FailMode = &mode
	bridge.ExternalIDs = map[string]string{ownerKey: ownerValue}
	changes, err := database.Create(&bridge)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"OVS bridge creation preparation failed",
			"bridge",
			bridge.Name,
			"err",
			err,
		)

		return nil, fmt.Errorf("prepare OVS bridge %q: %w", bridge.Name, err)
	}

	root.Bridges = append(root.Bridges, bridge.UUID)

	return changes, nil
}

func ownedPorts(
	ctx context.Context,
	database databaseClient,
	bridge bridgeRow,
	datapath string,
) (map[string]portRow, error) {
	result := make(map[string]portRow, len(bridge.Ports))
	if bridge.UUID == "" {
		return result, nil
	}

	if bridge.ExternalIDs[ownerKey] != ownerValue {
		return nil, fmt.Errorf("bridge %q: %w", bridge.Name, errBridgeOwner)
	}

	if bridge.DatapathType != datapath || bridge.FailMode == nil ||
		*bridge.FailMode != secureMode || len(bridge.Controllers) != 0 {
		return nil, fmt.Errorf("bridge %q: %w", bridge.Name, errBridgeSettings)
	}

	for _, portID := range bridge.Ports {
		var port portRow
		port.UUID = portID
		if err := database.Get(ctx, &port); err != nil {
			slog.ErrorContext(
				ctx,
				"OVS port lookup failed",
				"bridge",
				bridge.Name,
				"port_id",
				portID,
				"err",
				err,
			)

			return nil, fmt.Errorf("read port %s on OVS bridge %q: %w", portID, bridge.Name, err)
		}

		if port.ExternalIDs[ownerKey] != ownerValue || len(port.Interfaces) != 1 {
			return nil, fmt.Errorf("port %q: %w", port.Name, errPortOwner)
		}

		result[port.Name] = port
	}

	return result, nil
}

func portChanges(
	ctx context.Context,
	database databaseClient,
	current portRow,
	port ovsconfig.Port,
	index int,
) (string, []ovsdb.Operation, error) {
	var device interfaceRow
	device.Name = port.Name
	device.Type = port.Type
	device.Options = optionMap(port.Options)
	if current.UUID != "" {
		device.UUID = current.Interfaces[0]
		changes, err := database.Where(&device).Update(&device, &device.Type, &device.Options)
		if err != nil {
			slog.ErrorContext(ctx, "OVS interface update preparation failed", "err", err)

			return "", nil, fmt.Errorf("prepare interface update: %w", err)
		}

		return current.UUID, changes, nil
	}

	device.UUID = fmt.Sprintf("mwan_interface_%d", index)
	current = portRow{
		UUID:        fmt.Sprintf("mwan_port_%d", index),
		Name:        port.Name,
		Interfaces:  []string{device.UUID},
		ExternalIDs: map[string]string{ownerKey: ownerValue},
	}
	interfaces, err := database.Create(&device)
	if err != nil {
		slog.ErrorContext(ctx, "OVS interface insert preparation failed", "err", err)

		return "", nil, fmt.Errorf("prepare interface insert: %w", err)
	}

	ports, err := database.Create(&current)
	if err != nil {
		slog.ErrorContext(ctx, "OVS port insert preparation failed", "err", err)

		return "", nil, fmt.Errorf("prepare port insert: %w", err)
	}

	return current.UUID, append(interfaces, ports...), nil
}

func waitForPorts(
	ctx context.Context,
	database databaseClient,
	root switchRow,
	ports []ovsconfig.Port,
) error {
	ticker := time.NewTicker(statusInterval)
	defer ticker.Stop()
	var portErr error

	for {
		var switches []*switchRow
		if err := readRows(ctx, database, &switches); err != nil {
			slog.ErrorContext(
				ctx,
				"OVS setup operation failed",
				"operation",
				"read OVS configuration status",
				"err",
				err,
			)

			return fmt.Errorf("read OVS configuration status: %w", err)
		}

		if len(switches) == 1 && switches[0].UUID == root.UUID &&
			switches[0].CurrentConfig >= root.NextConfig {
			portErr = checkPorts(ctx, database, ports)
			if portErr == nil {
				return nil
			}
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"wait for ovs-vswitchd to apply OVSDB configuration: %w",
				errors.Join(ctx.Err(), portErr),
			)
		case <-ticker.C:
		}
	}
}

func checkPorts(ctx context.Context, database databaseClient, ports []ovsconfig.Port) error {
	var interfaces []*interfaceRow
	if err := readRows(ctx, database, &interfaces); err != nil {
		slog.ErrorContext(
			ctx,
			"OVS setup operation failed",
			"operation",
			"read OVS interface status",
			"err",
			err,
		)

		return fmt.Errorf("read OVS interface status: %w", err)
	}

	byName := make(map[string]*interfaceRow, len(interfaces))
	for _, device := range interfaces {
		byName[device.Name] = device
	}

	for _, port := range ports {
		device := byName[port.Name]
		if device != nil && device.Error != nil && *device.Error != "" {
			return fmt.Errorf(
				"interface %q: %w: %s",
				port.Name,
				errInterfaceRejected,
				*device.Error,
			)
		}

		if device == nil || device.OpenFlow == nil || *device.OpenFlow <= 0 {
			return fmt.Errorf("interface %q: %w", port.Name, errMissingPort)
		}

		if device.Type != port.Type || !maps.Equal(device.Options, optionMap(port.Options)) {
			return fmt.Errorf("interface %q: %w", port.Name, errInterfaceConfig)
		}
	}

	return nil
}

func optionMap(options []ovsconfig.Option) map[string]string {
	result := make(map[string]string, len(options))
	for _, option := range options {
		result[option.Name] = option.Value
	}

	return result
}
