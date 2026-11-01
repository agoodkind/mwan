package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/observation"
)

type observationOptions struct {
	check          string
	paths          string
	runtimePath    string
	pathsSpecified bool
}

func runSingleObservation(options observationOptions, input io.Reader, output, diagnostics io.Writer) int {
	if len(options.check) > 64*1024 || len(options.paths) > 64*1024 {
		fmt.Fprintln(diagnostics, "observe JSON input exceeds 64 KiB")
		return 2
	}
	var paths []observation.PathIdentity
	pathDecoder := json.NewDecoder(strings.NewReader(options.paths))
	pathDecoder.DisallowUnknownFields()
	if err := pathDecoder.Decode(&paths); err != nil {
		fmt.Fprintln(diagnostics, "observe requires valid path identities JSON")
		return 2
	}
	var remaining json.RawMessage
	if err := pathDecoder.Decode(&remaining); err != io.EOF {
		fmt.Fprintln(diagnostics, "observe requires exactly one path identities JSON array")
		return 2
	}
	if options.check == "" {
		input = io.LimitReader(input, 64*1024)
	} else {
		input = strings.NewReader(options.check)
	}
	decoder := json.NewDecoder(input)
	decoder.DisallowUnknownFields()
	var spec observation.CheckSpec
	if err := decoder.Decode(&spec); err != nil {
		fmt.Fprintln(diagnostics, "observe requires a valid CheckSpec JSON object")
		return 2
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		fmt.Fprintln(diagnostics, "observe requires exactly one CheckSpec JSON object")
		return 2
	}
	if err := observation.Validate(spec); err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	binary, err := os.Executable()
	if err != nil {
		fmt.Fprintln(diagnostics, "observe executable identity is unavailable")
		return 1
	}
	log, closer := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewJSONHandler(diagnostics, nil)}})
	defer func() { _ = closer.Close() }()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	runtimeConfig, err := observationRuntime(options.pathsSpecified, options.runtimePath, binary, paths)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	executor := observation.NewExecutor(runtimeConfig, log)
	result := executor.Run(ctx, spec)
	if err := json.NewEncoder(output).Encode(result); err != nil {
		fmt.Fprintln(diagnostics, "observe result output failed")
		return 1
	}
	return 0
}

func runContinuousObservation(settingsPath, configPath string, output, diagnostics io.Writer) int {
	settings, err := observation.LoadContinuousSettings(settingsPath)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	cfg, err := config.LoadPath(configPath)
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 2
	}
	log, closer := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewJSONHandler(diagnostics, nil)}})
	defer func() { _ = closer.Close() }()
	notifier, err := notify.New(cfg.Email, cfg.NotifyConfig(), log, "mwan-observe")
	if err != nil {
		fmt.Fprintln(diagnostics, err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	encoder := json.NewEncoder(output)
	summary, runError := observation.RunContinuous(ctx, settings, notifier, log, func(_ observation.CheckSpec, result observation.Result) error {
		return encoder.Encode(struct {
			Result observation.Result `json:"result"`
		}{Result: result})
	})
	if err := encoder.Encode(struct {
		Summary observation.Summary `json:"summary"`
	}{Summary: summary}); err != nil {
		fmt.Fprintln(diagnostics, "observe terminal output failed")
		return 1
	}
	if runError != nil {
		fmt.Fprintln(diagnostics, runError)
		return 1
	}
	return 0
}

func observationRuntime(pathsSpecified bool, runtimeFile, binary string, paths []observation.PathIdentity) (observation.RuntimeConfig, error) {
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
	if pathsSpecified {
		runtimeConfig.Paths = paths
	}
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
	if settings.CloudflareTokenFile != "" && !filepath.IsAbs(settings.CloudflareTokenFile) {
		return settings, fmt.Errorf("observe Cloudflare token file path must be absolute")
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
