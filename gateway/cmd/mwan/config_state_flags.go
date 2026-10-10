package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
)

type configStateFlags struct {
	root string
}

func parseConfigStateFlags(args []string, diagnostics io.Writer) (configStateFlags, error) {
	flags := configStateFlags{root: ""}
	flagSet := flag.NewFlagSet("mwan config-state", flag.ContinueOnError)
	flagSet.SetOutput(diagnostics)
	flagSet.StringVar(&flags.root, "root", "", "root directory for configuration files")
	if err := flagSet.Parse(args); err != nil {
		slog.Warn("config state flag parsing failed", "err", err)
		return configStateFlags{root: ""}, fmt.Errorf("parse config state flags: %w", err)
	}
	return flags, nil
}
