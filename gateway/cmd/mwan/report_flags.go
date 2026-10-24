package main

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"goodkind.io/mwan/internal/networkjson"
)

type reportFlags struct{ action, networkPath, schemaDir string }

func parseReportFlags(arguments []string, diagnostics io.Writer) (reportFlags, error) {
	if len(arguments) == 0 || (arguments[0] != "watch" && arguments[0] != "send") {
		return reportFlags{}, errors.New("usage: mwan report <watch|send> [--network path] [--schema-dir path]")
	}
	flags := reportFlags{action: arguments[0]}
	parser := flag.NewFlagSet("mwan report", flag.ContinueOnError)
	parser.SetOutput(diagnostics)
	parser.StringVar(&flags.networkPath, "network", networkjson.DefaultPath, "network configuration document")
	parser.StringVar(&flags.schemaDir, "schema-dir", networkjson.DefaultSchemaDir, "YANG schema directory")
	if err := parser.Parse(arguments[1:]); err != nil {
		return flags, err
	}
	if parser.NArg() != 0 {
		return flags, fmt.Errorf("unexpected report arguments: %v", parser.Args())
	}
	return flags, nil
}
