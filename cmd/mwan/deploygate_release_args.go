package main

import (
	"context"
	"fmt"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

func runReleaseCheck(arguments []string) int {
	cfg, args, err := config.LoadArguments(arguments)
	if err != nil {
		fmt.Fprintf(os.Stdout, "release configuration rejected: %v\n", err)
		return exitDeployGateFailed
	}
	if len(args) != 2 {
		printDeployGateUsage()
		return exitDeployGateUsage
	}
	id := connectionid.ID(args[0])
	if id == "" {
		fmt.Fprintln(os.Stdout, "release arguments rejected: connection ID is empty")
		return exitDeployGateUsage
	}
	previous := interfaceintent.Owner(args[1])
	if previous != interfaceintent.OwnerNetworkd && previous != interfaceintent.OwnerMWAN {
		fmt.Fprintln(os.Stdout, "release arguments rejected: previous owner must be networkd or mwan")
		return exitDeployGateUsage
	}
	return checkConnectionRelease(context.Background(), os.Stdout, cfg, id, previous)
}
