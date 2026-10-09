package main

import (
	"fmt"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/failovercheck"
)

const failoverCheckArgumentCount = 1

func runConfigurationGate(mode deployGateMode, rest []string) int {
	switch mode {
	case gateModeCheckNetworkd:
		return runNetworkdCheck(rest)
	case gateModeCheckRelease:
		return runReleaseCheck(rest)
	case gateModeCheckFirewall:
		return runFirewallCheck(rest)
	case gateModeInspectFirewall:
		return runFirewallInspect(rest)
	case gateModeCheckFailover:
		return runFailoverCheck(rest)
	case gateModeCheckEgress, gateModeWaitReboot, gateModeWaitEgress,
		gateModeWaitDeploy, gateModeCheckOwned, gateModeCheckNetwork:
		printDeployGateUsage()
		return exitDeployGateUsage
	}
	printDeployGateUsage()
	return exitDeployGateUsage
}

func runFailoverCheck(args []string) int {
	if len(args) != failoverCheckArgumentCount {
		printDeployGateUsage()
		return exitDeployGateUsage
	}
	cfg, _, err := config.LoadArguments([]string{"--config", args[0]})
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
