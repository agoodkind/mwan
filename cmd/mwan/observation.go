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
	"path/filepath"
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
	runtimeFile := flags.String("runtime-settings", "", "nonsecret RuntimeConfig JSON file")
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
	runtimeConfig, err := observationRuntime(flags, *runtimeFile, binary, paths)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	executor := observation.NewExecutor(runtimeConfig, log)
	result := executor.Run(ctx, spec)
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		fmt.Fprintln(os.Stderr, "observe result output failed")
		return 1
	}
	return 0
}

func observationRuntime(flags *flag.FlagSet, runtimeFile, binary string, paths []observation.PathIdentity) (observation.RuntimeConfig, error) {
	var runtimeConfig observation.RuntimeConfig
	if runtimeFile != "" {
		settings, loadErr := loadObservationRuntime(runtimeFile)
		if loadErr != nil {
			return runtimeConfig, loadErr
		}
		runtimeConfig = settings
	}
	if runtimeConfig.MachineIDPath == "" {
		runtimeConfig.MachineIDPath = "/etc/machine-id"
	}
	if runtimeConfig.ProbeBinary == "" {
		runtimeConfig.ProbeBinary = binary
	}
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "paths" {
			runtimeConfig.Paths = paths
		}
	})
	return runtimeConfig, nil
}

func loadObservationRuntime(path string) (observation.RuntimeConfig, error) {
	var settings observation.RuntimeConfig
	file, err := os.Open(path)
	if err != nil {
		return settings, fmt.Errorf("observe runtime settings cannot be opened")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64*1024 {
		return settings, fmt.Errorf("observe runtime settings require a regular file below 64 KiB")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 64*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&settings); err != nil {
		return settings, fmt.Errorf("observe runtime settings require valid RuntimeConfig JSON")
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return settings, fmt.Errorf("observe runtime settings require one JSON object")
	}
	for _, configured := range []string{settings.MachineIDPath, settings.ProbeBinary} {
		if configured == "" {
			continue
		}
		if !filepath.IsAbs(configured) {
			return settings, fmt.Errorf("observe identity and executable paths must be absolute")
		}
		information, statErr := os.Stat(configured)
		if statErr != nil || !information.Mode().IsRegular() {
			return settings, fmt.Errorf("observe configured identity or executable is unavailable")
		}
		if configured == settings.ProbeBinary && information.Mode().Perm()&0o111 == 0 {
			return settings, fmt.Errorf("observe configured probe binary is not executable")
		}
	}
	return settings, nil
}
