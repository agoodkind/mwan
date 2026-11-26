package commandline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"goodkind.io/mwan/gateway/connectionid"
	"goodkind.io/mwan/gateway/interfaceintent"
	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/netif"
	"goodkind.io/mwan/gateway/internal/networkd"
	"goodkind.io/mwan/gateway/networkjson"
	"goodkind.io/mwan/gateway/networkload"
)

func runCheckRelease(
	ctx context.Context,
	output io.Writer,
	path string,
	connectionID connectionid.ID,
	previous interfaceintent.Owner,
) int {
	cfg, err := config.LoadPath(path)
	if err != nil {
		fmt.Fprintf(output, "release configuration rejected: %v\n", err)

		return exitCheckFailed
	}

	return checkConnectionRelease(
		ctx,
		output,
		cfg,
		cfg.IfMgr.NetworkdTimeout(),
		connectionID,
		previous,
	)
}

func checkConnectionRelease(
	ctx context.Context,
	output io.Writer,
	cfg *config.Config,
	timeout time.Duration,
	connectionID connectionid.ID,
	previous interfaceintent.Owner,
) int {
	loaded, err := networkload.Load(networkjson.DefaultPath, networkjson.DefaultSchemaDir)
	if err != nil {
		fmt.Fprintf(output, "release verification failed: %v\n", err)

		return exitCheckFailed
	}

	selected := findReleaseConnection(loaded.Connections, connectionID)
	if selected == nil {
		fmt.Fprintf(
			output,
			"release verification failed: connection %s is absent or rejected\n",
			connectionID,
		)

		return exitCheckFailed
	}

	if selected.Owner != interfaceintent.OwnerExternal {
		fmt.Fprintf(
			output,
			"release verification failed: connection %s owner is %s, required external\n",
			connectionID,
			selected.Owner,
		)

		return exitCheckFailed
	}

	if !verifyPreviousNetworkdOwner(ctx, output, timeout, connectionID, selected, previous) {
		return exitCheckFailed
	}

	return inspectConnectionRelease(output, cfg, connectionID)
}

func findReleaseConnection(
	connections []interfaceintent.Connection,
	connectionID connectionid.ID,
) *interfaceintent.Connection {
	for index := range connections {
		connection := &connections[index]
		if connection.ID == connectionID {
			return connection
		}
	}

	return nil
}

func verifyPreviousNetworkdOwner(
	ctx context.Context,
	output io.Writer,
	timeout time.Duration,
	connectionID connectionid.ID,
	selected *interfaceintent.Connection,
	previous interfaceintent.Owner,
) bool {
	if previous != interfaceintent.OwnerNetworkd {
		return true
	}

	if timeout <= 0 {
		fmt.Fprintln(
			output,
			"release verification failed: [ifmgr].networkd_timeout_seconds must be positive",
		)

		return false
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	state, err := networkd.AdministrativeState(ctx, selected.Name)
	if err != nil {
		fmt.Fprintf(output, "release verification failed: %v\n", err)

		return false
	}

	fmt.Fprintf(
		output,
		"connection %s interface %s networkd administrative state: %s\n",
		connectionID,
		selected.Name,
		state,
	)

	return state == "unmanaged"
}

func inspectConnectionRelease(
	output io.Writer,
	cfg *config.Config,
	connectionID connectionid.ID,
) int {
	modules := cfg.IfMgr.Modules
	if modules.Links == nil || modules.Addresses == nil || modules.Autoconfiguration == nil {
		fmt.Fprintln(
			output,
			"release verification failed: links, addresses, and autoconfiguration journal settings are required",
		)

		return exitCheckFailed
	}

	receipts, err := netif.InspectOwnedRelease(
		connectionID,
		modules.Links.StateFile,
		modules.Addresses.StateFile,
		modules.Autoconfiguration.StateFile,
	)
	if err != nil {
		fmt.Fprintf(output, "release verification failed: %v\n", err)

		return exitCheckFailed
	}

	if err := json.NewEncoder(output).Encode(receipts); err != nil {
		return exitCheckFailed
	}

	if !receipts.Released() {
		fmt.Fprintf(
			output,
			"release verification failed: connection %s has pending or previous-boot receipts\n",
			connectionID,
		)

		return exitCheckFailed
	}

	fmt.Fprintf(
		output,
		"connection %s ordinary ownership receipts are released; "+
			"daemon configuration and client cessation require separate verification\n",
		connectionID,
	)

	return exitCheckOK
}
