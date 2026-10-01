package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/netif"
	"goodkind.io/mwan/internal/networkd"
	"goodkind.io/mwan/internal/networkjson"
)

func checkConnectionRelease(ctx context.Context, output io.Writer, cfg *config.Config, id connectionid.ID, previous interfaceintent.Owner) int {
	loaded, err := networkjson.Load(networkjson.DefaultPath, networkjson.DefaultSchemaDir)
	if err != nil {
		fmt.Fprintf(output, "release verification failed: %v\n", err)
		return exitDeployGateFailed
	}
	var selected *interfaceintent.Connection
	for index := range loaded.Connections {
		connection := &loaded.Connections[index]
		if connection.ID == id {
			selected = connection
			break
		}
	}
	if selected == nil {
		fmt.Fprintf(output, "release verification failed: connection %s is absent or rejected\n", id)
		return exitDeployGateFailed
	}
	if selected.Owner != interfaceintent.OwnerExternal {
		fmt.Fprintf(output, "release verification failed: connection %s owner is %s, required external\n", id, selected.Owner)
		return exitDeployGateFailed
	}
	if previous == interfaceintent.OwnerNetworkd {
		state, err := networkd.AdministrativeState(ctx, selected.Name)
		if err != nil {
			fmt.Fprintf(output, "release verification failed: %v\n", err)
			return exitDeployGateFailed
		}
		fmt.Fprintf(output, "connection %s interface %s networkd administrative state: %s\n", id, selected.Name, state)
		if state != "unmanaged" {
			return exitDeployGateFailed
		}
		return exitDeployGateOK
	}
	modules := cfg.IfMgr.Modules
	if modules.Links == nil || modules.Addresses == nil || modules.Autoconfiguration == nil {
		fmt.Fprintln(output, "release verification failed: links, addresses, and autoconfiguration journal settings are required")
		return exitDeployGateFailed
	}
	receipts, err := netif.InspectOwnedRelease(id, modules.Links.StateFile, modules.Addresses.StateFile, modules.Autoconfiguration.StateFile)
	if err != nil {
		fmt.Fprintf(output, "release verification failed: %v\n", err)
		return exitDeployGateFailed
	}
	if err := json.NewEncoder(output).Encode(receipts); err != nil {
		return exitDeployGateFailed
	}
	if !receipts.Released() {
		fmt.Fprintf(output, "release verification failed: connection %s has pending or previous-boot receipts\n", id)
		return exitDeployGateFailed
	}
	fmt.Fprintf(output, "connection %s ordinary ownership receipts are released; daemon configuration and client cessation require separate verification\n", id)
	return exitDeployGateOK
}
