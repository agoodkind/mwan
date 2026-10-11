package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/configstate"
)

func runConfigState(
	args []string,
	cfg *config.Config,
	output io.Writer,
	diagnostics io.Writer,
) int {
	flags, err := parseConfigStateFlags(args, diagnostics)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan config-state: %v\n", err)
		return 1
	}
	// A missing deploy file produces LastDeployEpoch 0.
	deployFile, _ := os.ReadFile(cfg.Agent.DeployFile)
	state := configstate.Read(flags.root, deployFile)
	if err := json.NewEncoder(output).Encode(state); err != nil {
		fmt.Fprintf(diagnostics, "mwan config-state: encode config state: %v\n", err)
		return 1
	}
	return 0
}
