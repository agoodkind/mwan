package main

import (
	"fmt"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/networkd"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/networkload"
)

func runNetworkdCheck(args []string) int {
	if len(args) != 3 {
		printDeployGateUsage()
		return exitDeployGateUsage
	}
	return checkNetworkd(args[0], args[1], args[2])
}

func checkNetworkd(path string, schemaDir string, unitDir string) int {
	loaded, err := networkload.Load(path, schemaDir)
	if err != nil {
		fmt.Fprintf(os.Stdout, "network configuration rejected: %v\n", err)
		return exitDeployGateFailed
	}
	if len(loaded.Rejected) != 0 {
		for _, rejected := range loaded.Rejected {
			fmt.Fprintf(os.Stdout, "provider entry rejected: interface %s: %v\n", rejected.Interface, rejected.Err)
		}
		return exitDeployGateFailed
	}
	tables := make(map[connectionid.ID]int, len(loaded.WAN))
	for id, provider := range loaded.WAN {
		tables[connectionid.ID(id)] = provider.TableID
	}
	if err := networkd.VerifyDir(unitDir, loaded.Connections, tables, networkdNPTPreservation(loaded)); err != nil {
		fmt.Fprintf(os.Stdout, "networkd unit verification failed: %v\n", err)
		return exitDeployGateFailed
	}
	fmt.Fprintf(os.Stdout, "networkd units match configured intent: %s\n", unitDir)
	return exitDeployGateOK
}

func networkdNPTPreservation(loaded *networkjson.Config) map[connectionid.ID]bool {
	preserveStatic := make(map[connectionid.ID]bool)
	for id, provider := range loaded.WAN {
		if provider.TranslationV6 != nil && provider.TranslationV6.Mode == config.TranslationNPTv6 {
			preserveStatic[connectionid.ID(id)] = true
		}
	}
	return preserveStatic
}
