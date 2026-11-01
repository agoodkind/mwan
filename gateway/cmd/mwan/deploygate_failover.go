package main

import (
	"fmt"
	"io"

	"goodkind.io/mwan/internal/failovercheck"
	"goodkind.io/mwan/internal/watchdogconfig"
)

func checkFailover(path string, diagnostics io.Writer) int {
	cfg, err := watchdogconfig.LoadPath(path)
	if err != nil {
		fmt.Fprintf(diagnostics, "failover configuration rejected: %v\n", err)
		return exitDeployGateFailed
	}
	failures := failovercheck.Check(cfg)
	for _, failure := range failures {
		fmt.Fprintln(diagnostics, failure)
	}
	if len(failures) > 0 {
		return exitDeployGateFailed
	}
	return exitDeployGateOK
}
