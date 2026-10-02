package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/deployoperation"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
)

type deployOperationStatus struct {
	deployoperation.Record
	MutationReady bool   `json:"mutation_ready"`
	WatchError    string `json:"watch_error,omitempty"`
}

func runDeployOperation(arguments []string) int {
	cfg, args, err := config.LoadArguments(arguments)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan deploy-gate operation: %v\n", err)
		return exitDeployGateUsage
	}
	if len(args) == 0 || cfg.Watchdog.ConnectivityTimeoutSeconds <= 0 || cfg.Watchdog.CheckIntervalDegraded <= 0 {
		fmt.Fprintln(os.Stderr, "mwan deploy-gate operation: a mode and positive watchdog timeouts are required")
		return exitDeployGateUsage
	}
	logger, closer := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewJSONHandler(os.Stderr, nil)}})
	defer func() { _ = closer.Close() }()
	operations := ops.NewRealOps(cfg, logger)
	runtimePath := ""
	for index, argument := range arguments {
		if argument == "--config" && index+1 < len(arguments) {
			runtimePath = arguments[index+1]
		}
	}
	if args[0] == "arm" && !filepath.IsAbs(runtimePath) {
		fmt.Fprintln(os.Stderr, "mwan deploy-gate arm requires --config with an absolute runtime configuration path")
		return exitDeployGateUsage
	}
	engine := deployoperation.Engine{
		Store:        deployoperation.Store{Path: cfg.Watchdog.RollbackLockFile + ".operation", PollInterval: time.Duration(cfg.Watchdog.CheckIntervalDegraded) * time.Second},
		RollbackLock: cfg.Watchdog.RollbackLockFile,
		Operations:   operations,
		Log:          logger,
		Notify:       notify.FromConfig(cfg, logger, "mwan-deploy-gate"),
		RuntimePath:  runtimePath,
	}
	signalContext, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, time.Duration(cfg.Watchdog.ConnectivityTimeoutSeconds)*time.Second)
	defer cancel()
	err = executeDeployOperation(ctx, signalContext, cfg, engine, args)
	if err != nil {
		logger.Error("deployment operation failed", "err", err)
		return exitDeployGateFailed
	}
	return exitDeployGateOK
}

func executeDeployOperation(ctx, signalContext context.Context, cfg *config.Config, engine deployoperation.Engine, args []string) error {
	mode := args[0]
	if mode == "arm" {
		if len(args) != 2 {
			return fmt.Errorf("arm requires one manifest path")
		}
		manifest, err := deployoperation.ReadManifest(args[1])
		if err != nil {
			return err
		}
		if manifest.VMID != cfg.MwanVMID {
			return fmt.Errorf("deployment manifest VMID differs from configured gateway")
		}
		if err := engine.Arm(ctx, manifest); err != nil {
			return err
		}
		return writeDeployOperationStatus(ctx, engine)
	}
	if len(args) < 3 {
		return fmt.Errorf("operation mode requires exact operation ID and generation")
	}
	record, err := engine.Store.Read(ctx)
	if err != nil {
		return err
	}
	if record.OperationID != args[1] || record.Generation != args[2] || record.VMID != cfg.MwanVMID {
		return fmt.Errorf("deployment operation identity differs from request or configured gateway")
	}
	switch mode {
	case "status":
		if len(args) != 3 {
			return fmt.Errorf("status requires operation ID and generation only")
		}
	case "watch":
		if len(args) != 3 {
			return fmt.Errorf("watch requires operation ID and generation only")
		}
		watchContext, watchCancel := context.WithDeadline(signalContext, record.Deadline.Add(time.Duration(record.RecoveryTimeoutSeconds)*time.Second))
		defer watchCancel()
		return engine.Watch(watchContext, record.OperationID, record.Generation)
	case "lease":
		if len(args) != 5 {
			return fmt.Errorf("lease requires operation ID, generation, phase and positive duration seconds")
		}
		seconds, err := strconv.Atoi(args[4])
		if err != nil || seconds <= 0 {
			return fmt.Errorf("lease duration must be positive integer seconds")
		}
		lease, err := engine.Store.Grant(ctx, record.OperationID, record.Generation, args[3], time.Now().Add(time.Duration(seconds)*time.Second))
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(lease)
	case "release":
		if len(args) != 4 {
			return fmt.Errorf("release requires operation ID, generation and lease ID")
		}
		if err := engine.Store.Release(ctx, record.OperationID, record.Generation, args[3]); err != nil {
			return err
		}
	case "commit":
		if len(args) != 3 {
			return fmt.Errorf("commit requires operation ID and generation only")
		}
		if err := engine.Commit(ctx, record.OperationID, record.Generation); err != nil {
			return err
		}
	case "recover":
		if len(args) != 3 {
			return fmt.Errorf("recover requires operation ID and generation only")
		}
		recoveryContext, recoveryCancel := context.WithTimeout(signalContext, time.Duration(record.RecoveryTimeoutSeconds)*time.Second)
		defer recoveryCancel()
		if err := engine.Recover(recoveryContext, record.OperationID, record.Generation, "explicit exact-operation recovery requested"); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported deployment operation mode %s", mode)
	}
	return writeDeployOperationStatus(ctx, engine)
}

func writeDeployOperationStatus(ctx context.Context, engine deployoperation.Engine) error {
	record, err := engine.Store.Read(ctx)
	if err != nil {
		return err
	}
	status := deployOperationStatus{Record: record, MutationReady: record.MutationReady(time.Now())}
	if status.MutationReady {
		if err := record.Watch.Verify(ctx); err != nil {
			status.MutationReady = false
			status.WatchError = err.Error()
		}
	}
	return json.NewEncoder(os.Stdout).Encode(status)
}
