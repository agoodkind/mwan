package main

import (
	"fmt"
	"io"
	"os"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/configstate"
)

func runConfigStateOptions(
	flags configStateFlags,
	cfg *config.Config,
	output io.Writer,
	diagnostics io.Writer,
) int {
	logger, closer := operatorLogger(diagnostics, flags.verbose)
	defer closer.Close()
	// A missing deploy file produces LastDeployEpoch 0.
	deployFile, _ := os.ReadFile(cfg.Agent.DeployFile)
	state := configstate.Read(flags.root, deployFile)
	var err error
	if flags.outputFormat == "text" {
		err = WriteConfigStateText(logger, output, state)
	} else {
		err = WriteJSON(logger, output, state)
	}

	if err != nil {
		fmt.Fprintf(diagnostics, "mwan config-state: encode config state: %v\n", err)
		return 1
	}

	return 0
}
