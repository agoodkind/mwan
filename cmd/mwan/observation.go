package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/observation"
)

// A target failure is a JSON result. Invalid input or failed output is a command error.
func runObservation(args []string) int {
	flags := flag.NewFlagSet("observe", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	checkJSON := flags.String("check", "", "CheckSpec JSON; empty reads standard input")
	pathsJSON := flags.String("paths", "[]", "current typed path identities as JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "observe accepts no positional arguments")
		return 2
	}
	if len(*checkJSON) > 64*1024 || len(*pathsJSON) > 64*1024 {
		fmt.Fprintln(os.Stderr, "observe JSON input exceeds 64 KiB")
		return 2
	}
	var paths []observation.PathIdentity
	pathDecoder := json.NewDecoder(strings.NewReader(*pathsJSON))
	pathDecoder.DisallowUnknownFields()
	if err := pathDecoder.Decode(&paths); err != nil {
		fmt.Fprintln(os.Stderr, "observe requires valid path identities JSON")
		return 2
	}
	var remaining json.RawMessage
	if err := pathDecoder.Decode(&remaining); err != io.EOF {
		fmt.Fprintln(os.Stderr, "observe requires exactly one path identities JSON array")
		return 2
	}
	var input io.Reader = strings.NewReader(*checkJSON)
	if *checkJSON == "" {
		input = io.LimitReader(os.Stdin, 64*1024)
	}
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	var spec observation.CheckSpec
	if err := decoder.Decode(&spec); err != nil {
		fmt.Fprintln(os.Stderr, "observe requires a valid CheckSpec JSON object")
		return 2
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		fmt.Fprintln(os.Stderr, "observe requires exactly one CheckSpec JSON object")
		return 2
	}
	if err := observation.Validate(spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "observe executable identity is unavailable")
		return 1
	}
	log, closer := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewJSONHandler(os.Stderr, nil)}})
	defer func() { _ = closer.Close() }()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var runtimeConfig observation.RuntimeConfig
	runtimeConfig.MachineIDPath, runtimeConfig.ProbeBinary, runtimeConfig.Paths = "/etc/machine-id", binary, paths
	executor := observation.NewExecutor(runtimeConfig, log)
	result := executor.Run(ctx, spec)
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "observe result output failed")
		return 1
	}
	return 0
}
