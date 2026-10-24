package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/mwan/internal/networkjson"
)

type reportFlags struct{ action, networkPath, schemaDir string }

func parseReportFlags(arguments []string, diagnostics io.Writer) (reportFlags, error) {
	if len(arguments) == 0 || (arguments[0] != "watch" && arguments[0] != "send") {
		var empty reportFlags
		return empty, errors.New("usage: mwan report <watch|send> [--network path] [--schema-dir path]")
	}
	flags := reportFlags{action: arguments[0], networkPath: "", schemaDir: ""}
	parser := flag.NewFlagSet("mwan report", flag.ContinueOnError)
	parser.SetOutput(diagnostics)
	parser.StringVar(&flags.networkPath, "network", networkjson.DefaultPath, "network configuration document")
	parser.StringVar(&flags.schemaDir, "schema-dir", networkjson.DefaultSchemaDir, "YANG schema directory")
	if err := parser.Parse(arguments[1:]); err != nil {
		slog.Error("parse report flags failed", "error", err)
		return flags, fmt.Errorf("parse report flags: %w", err)
	}
	if parser.NArg() != 0 {
		return flags, fmt.Errorf("unexpected report arguments: %v", parser.Args())
	}
	return flags, nil
}
