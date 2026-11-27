package ovs

// Bridge setup uses this OVSDB client to write configuration. Bridge setup
// and OpenFlow installation use its row models to follow references from a
// bridge to its ports and from each port to its interfaces.

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ovn-kubernetes/libovsdb/client"
	"github.com/ovn-kubernetes/libovsdb/model"
	"github.com/ovn-kubernetes/libovsdb/ovsdb"
)

const (
	ownerKey   = "mwan-owner"
	ownerValue = "mwan"
)

type databaseClient struct {
	client.Client
}

type switchRow struct {
	UUID          string            `ovsdb:"_uuid"`
	Bridges       []string          `ovsdb:"bridges"`
	OtherConfig   map[string]string `ovsdb:"other_config"`
	DatapathTypes []string          `ovsdb:"datapath_types"`
	CurrentConfig int               `ovsdb:"cur_cfg"`
	NextConfig    int               `ovsdb:"next_cfg"`
}

type bridgeRow struct {
	UUID         string            `ovsdb:"_uuid"`
	Name         string            `ovsdb:"name"`
	DatapathType string            `ovsdb:"datapath_type"`
	FailMode     *string           `ovsdb:"fail_mode"`
	Ports        []string          `ovsdb:"ports"`
	ExternalIDs  map[string]string `ovsdb:"external_ids"`
	Controllers  []string          `ovsdb:"controller"`
	Protocols    []string          `ovsdb:"protocols"`
	DatapathID   *string           `ovsdb:"datapath_id"`
}

type portRow struct {
	UUID        string            `ovsdb:"_uuid"`
	Name        string            `ovsdb:"name"`
	Interfaces  []string          `ovsdb:"interfaces"`
	ExternalIDs map[string]string `ovsdb:"external_ids"`
}

type interfaceRow struct {
	UUID     string            `ovsdb:"_uuid"`
	Name     string            `ovsdb:"name"`
	Type     string            `ovsdb:"type"`
	Options  map[string]string `ovsdb:"options"`
	OpenFlow *int              `ovsdb:"ofport"`
	Error    *string           `ovsdb:"error"`
}

func connect(ctx context.Context, socket string) (databaseClient, error) {
	database, err := model.NewClientDBModel(
		"Open_vSwitch",
		map[string]model.Model{
			"Open_vSwitch": new(switchRow),
			"Bridge":       new(bridgeRow),
			"Port":         new(portRow),
			"Interface":    new(interfaceRow),
		},
	)
	if err != nil {
		slog.ErrorContext(ctx, "OVSDB model creation failed", "err", err)

		return databaseClient{}, fmt.Errorf("define OVSDB client model: %w", err)
	}

	connection, err := client.NewOVSDBClient(database, client.WithEndpoint("unix:"+socket))
	if err != nil {
		slog.ErrorContext(ctx, "OVSDB client creation failed", "socket", socket, "err", err)

		return databaseClient{}, fmt.Errorf("configure OVSDB client: %w", err)
	}

	if err := connection.Connect(ctx); err != nil {
		connection.Close()
		slog.ErrorContext(ctx, "OVSDB connection failed", "socket", socket, "err", err)

		return databaseClient{}, fmt.Errorf("connect to OVSDB socket %s: %w", socket, err)
	}

	if _, err := connection.MonitorAll(ctx); err != nil {
		connection.Close()
		slog.ErrorContext(ctx, "OVSDB monitor failed", "socket", socket, "err", err)

		return databaseClient{}, fmt.Errorf("monitor OVSDB: %w", err)
	}

	return databaseClient{Client: connection}, nil
}

func transact(ctx context.Context, database databaseClient, operations []ovsdb.Operation) error {
	results, err := database.Transact(ctx, operations...)
	if err != nil {
		slog.ErrorContext(ctx, "OVSDB write failed", "err", err)

		return fmt.Errorf("write OVSDB configuration: %w", err)
	}

	failures, err := ovsdb.CheckOperationResults(results, operations)
	if err != nil {
		slog.ErrorContext(ctx, "OVSDB rejected configuration", "err", err, "failures", failures)

		return fmt.Errorf("OVSDB rejected configuration changes: %w (%v)", err, failures)
	}

	return nil
}

type statusRow interface {
	switchRow | interfaceRow | bridgeRow | portRow
}

func readRows[Row statusRow](ctx context.Context, database databaseClient, target *[]*Row) error {
	operations, results, err := selectRows(ctx, database, new(Row))
	if err != nil {
		return err
	}

	*target, err = decodeRows[Row](ctx, database, operations, results)

	return err
}

func decodeRows[Row statusRow](
	ctx context.Context,
	database databaseClient,
	operations []ovsdb.Operation,
	results []ovsdb.OperationResult,
) ([]*Row, error) {
	var rows []*Row
	if err := database.GetSelectResults(operations, results, &rows); err != nil {
		slog.ErrorContext(ctx, "OVSDB result decoding failed", "err", err)

		return nil, fmt.Errorf("decode OVSDB select results: %w", err)
	}

	return rows, nil
}

func selectRows(
	ctx context.Context,
	database databaseClient,
	rows ...model.Model,
) ([]ovsdb.Operation, []ovsdb.OperationResult, error) {
	var operations []ovsdb.Operation

	for _, row := range rows {
		selection, err := database.Select(row)
		if err != nil {
			slog.ErrorContext(ctx, "OVSDB select preparation failed", "err", err)

			return nil, nil, fmt.Errorf("prepare OVSDB select: %w", err)
		}

		operations = append(operations, selection...)
	}

	results, err := database.Transact(ctx, operations...)
	if err != nil {
		slog.ErrorContext(ctx, "OVSDB select failed", "err", err)

		return nil, nil, fmt.Errorf("read OVSDB rows: %w", err)
	}

	if _, err := ovsdb.CheckOperationResults(results, operations); err != nil {
		slog.ErrorContext(ctx, "OVSDB rejected select", "err", err)

		return nil, nil, fmt.Errorf("OVSDB rejected a select operation: %w", err)
	}

	return operations, results, nil
}
