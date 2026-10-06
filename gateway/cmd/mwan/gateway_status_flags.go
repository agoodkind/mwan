package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/mwan/internal/networkjson"
)

type gatewayStatusFlags struct {
	networkPath string
	schemaDir   string
}

func parseGatewayStatusFlags(args []string, diagnostics io.Writer) (gatewayStatusFlags, error) {
	flags := gatewayStatusFlags{networkPath: "", schemaDir: ""}
	flagSet := flag.NewFlagSet("mwan gateway-status", flag.ContinueOnError)
	flagSet.SetOutput(diagnostics)
	flagSet.StringVar(&flags.networkPath, "network", networkjson.DefaultPath,
		"network configuration document")
	flagSet.StringVar(&flags.schemaDir, "schema-dir", networkjson.DefaultSchemaDir,
		"directory holding the YANG models that validate the network document")
	if err := flagSet.Parse(args); err != nil {
		slog.Warn("gateway-status: parse flags failed", "err", err)
		return gatewayStatusFlags{networkPath: "", schemaDir: ""},
			fmt.Errorf("parse flags: %w", err)
	}
	return flags, nil
}
