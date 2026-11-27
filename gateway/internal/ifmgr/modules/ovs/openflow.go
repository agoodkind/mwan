package ovs

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"net"
	"slices"
	"time"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

func installOpenFlow(
	ctx context.Context,
	database databaseClient,
	config ovsconfig.Config,
	health map[string]wanstate.MemberHealth,
) error {
	operations, results, err := selectRows(
		ctx,
		database,
		new(interfaceRow),
		new(portRow),
		new(bridgeRow),
	)
	if err != nil {
		return err
	}

	interfaces, err := decodeRows[interfaceRow](ctx, database, operations, results)
	if err != nil {
		return err
	}

	ports, err := decodeRows[portRow](ctx, database, operations, results)
	if err != nil {
		return err
	}

	bridges, err := decodeRows[bridgeRow](ctx, database, operations, results)
	if err != nil {
		return err
	}

	bridge, err := readOpenFlowBridge(ctx, bridges, config.Bridge)
	if err != nil {
		return err
	}

	numbers := bridgePortNumbers(bridge, ports, interfaces, config.Ports)

	return installOnSwitch(ctx, config.OpenFlow, *bridge.DatapathID, numbers, health)
}

func bridgePortNumbers(
	bridge *bridgeRow,
	ports []*portRow,
	interfaces []*interfaceRow,
	configured []ovsconfig.Port,
) map[string]uint32 {
	members := make(map[string]bool)

	for _, port := range ports {
		if slices.Contains(bridge.Ports, port.UUID) {
			for _, id := range port.Interfaces {
				members[id] = true
			}
		}
	}

	names := make(map[string]bool, len(configured))
	for _, port := range configured {
		names[port.Name] = true
	}

	numbers := make(map[string]uint32)

	for _, device := range interfaces {
		if members[device.UUID] && names[device.Name] && device.OpenFlow != nil &&
			*device.OpenFlow > 0 &&
			*device.OpenFlow <= math.MaxUint32 {
			numbers[device.Name] = uint32(*device.OpenFlow)
		}
	}

	return numbers
}

func readOpenFlowBridge(
	ctx context.Context,
	bridges []*bridgeRow,
	bridgeName string,
) (*bridgeRow, error) {
	for _, bridge := range bridges {
		if bridge.Name == bridgeName && bridge.DatapathID != nil {
			return bridge, nil
		}
	}

	slog.ErrorContext(
		ctx,
		"OVSDB did not report a datapath ID",
		"bridge",
		bridgeName,
		"err",
		errOpenFlowBridge,
	)

	return nil, fmt.Errorf(
		"%w: OVSDB did not report a datapath ID for %q",
		errOpenFlowBridge,
		bridgeName,
	)
}

func installOnSwitch(
	ctx context.Context,
	config *ovsconfig.OpenFlowConfig,
	bridgeID string,
	numbers map[string]uint32,
	health map[string]wanstate.MemberHealth,
) error {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", config.Socket)
	if err != nil {
		slog.ErrorContext(ctx, "OpenFlow connection failed", "socket", config.Socket, "err", err)

		return fmt.Errorf("connect to OpenFlow socket %s: %w", config.Socket, err)
	}

	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			slog.DebugContext(ctx, "OpenFlow connection close failed", "err", closeErr)
		}
	}()

	deadline, _ := ctx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		slog.ErrorContext(ctx, "OpenFlow deadline failed", "err", err)

		return fmt.Errorf("set OpenFlow deadline: %w", err)
	}

	stop := context.AfterFunc(
		ctx,
		func() {
			if deadlineErr := conn.SetDeadline(time.Unix(0, 0)); deadlineErr != nil {
				slog.DebugContext(ctx, "OpenFlow cancellation failed", "err", deadlineErr)
			}
		},
	)
	defer stop()

	var installed installedEntries
	if err := checkOpenFlowBridge(ctx, conn, bridgeID, &installed.references); err != nil {
		return err
	}

	installed.flows, err = readFlows(ctx, conn)
	if err != nil {
		return err
	}

	var translations *installedEntries
	if len(referencedMappings(config)) > 0 {
		translations = &installed
		installed.dependencies = make(map[uint32][]bucketProgram)
	}

	installed.groups, err = readGroups(ctx, conn, translations)
	if err != nil {
		return err
	}

	flows, groups, err := compileOpenFlow(ctx, config, numbers, health, installed)
	if err != nil {
		return err
	}

	same, err := rulesMatch(installed.flows, flows[1:])
	if err != nil {
		return err
	}

	changes, err := groupChanges(installed.groups, groups)
	if err != nil || (same && len(changes) == 0) {
		return err
	}

	if err := replaceEntries(ctx, conn, flows, changes); err != nil {
		slog.ErrorContext(ctx, "OpenFlow bundle failed", "bridge", bridgeID, "err", err)

		return fmt.Errorf("apply OpenFlow bundle: %w", err)
	}

	return nil
}
