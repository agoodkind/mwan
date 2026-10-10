package main

import (
	"fmt"
	"os"

	"goodkind.io/mwan/internal/failovercheck"
	"goodkind.io/mwan/internal/watchdogconfig"
)

const failoverCheckArgumentCount = 1

func runFailoverCheck(args []string) int {
	if len(args) != failoverCheckArgumentCount {
		printDeployGateUsage()
		return exitDeployGateUsage
	}
	cfg, _, err := watchdogconfig.LoadArguments([]string{"--config", args[0]})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failover configuration rejected: %v\n", err)
		return exitDeployGateFailed
	}
	failures := failovercheck.Check(cfg)
	for _, failure := range failures {
		fmt.Fprintln(os.Stderr, failure)
	}
	if len(failures) > 0 {
		return exitDeployGateFailed
	}
	return exitDeployGateOK
}
