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

	"goodkind.io/mwan/internal/clock"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/deployoperation"
	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/notify"
	"goodkind.io/mwan/internal/ops"
)

type operationMode string

const (
	operationArm     operationMode = "arm"
	operationWatch   operationMode = "watch"
	operationStatus  operationMode = "status"
	operationLease   operationMode = "lease"
	operationRelease operationMode = "release"
	operationCommit  operationMode = "commit"
	operationRecover operationMode = "recover"
)

type currentOperationMode string

const (
	currentWatch   currentOperationMode = "watch"
	currentStatus  currentOperationMode = "status"
	currentLease   currentOperationMode = "lease"
	currentRelease currentOperationMode = "release"
	currentCommit  currentOperationMode = "commit"
	currentRecover currentOperationMode = "recover"
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
		Store:        deployoperation.Store{Path: cfg.Watchdog.RollbackLockFile + ".operation", PollInterval: time.Duration(cfg.Watchdog.CheckIntervalDegraded) * time.Second, Clock: clock.Real{}},
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
	switch operationMode(args[0]) {
	case operationArm:
		return armDeployOperation(ctx, cfg, engine, args)
	case operationWatch, operationStatus, operationLease, operationRelease, operationCommit, operationRecover:
		return executeCurrentDeployOperation(ctx, signalContext, cfg, engine, args)
	default:
		return fmt.Errorf("unsupported deployment operation mode %s", args[0])
	}
}

func executeCurrentDeployOperation(ctx, signalContext context.Context, cfg *config.Config, engine deployoperation.Engine, args []string) (resultErr error) {
	defer func() {
		if resultErr != nil {
			engine.Log.WarnContext(ctx, "deployment operation command rejected")
		}
	}()
	mode := currentOperationMode(args[0])
	if err := validateExistingOperationArguments(operationMode(mode), args); err != nil {
		return err
	}
	record, err := engine.Store.Read(ctx)
	if err != nil {
		return fmt.Errorf("execute deployment operation: %w", err)
	}
	if record.OperationID != args[1] || record.Generation != args[2] || record.VMID != cfg.MwanVMID {
		return fmt.Errorf("deployment operation identity differs from request or configured gateway")
	}
	switch mode {
	case currentStatus:

	case currentWatch:

		watchContext, watchCancel := context.WithDeadline(signalContext, record.Deadline.Add(time.Duration(record.RecoveryTimeoutSeconds)*time.Second))
		defer watchCancel()
		if err := engine.Watch(watchContext, record.OperationID, record.Generation); err != nil {
			return fmt.Errorf("watch deployment operation: %w", err)
		}
		return nil
	case currentLease:

		seconds, err := strconv.Atoi(args[4])
		if err != nil || seconds <= 0 {
			return fmt.Errorf("lease duration must be positive integer seconds")
		}
		lease, err := engine.Store.Grant(ctx, record.OperationID, record.Generation, args[3], engine.Store.Clock.Now().Add(time.Duration(seconds)*time.Second))
		if err != nil {
			return fmt.Errorf("execute deployment operation: %w", err)
		}
		if err := json.NewEncoder(os.Stdout).Encode(lease); err != nil {
			return fmt.Errorf("write deployment lease: %w", err)
		}
		return nil
	case currentRelease:

		if err := engine.Store.Release(ctx, record.OperationID, record.Generation, args[3]); err != nil {
			return fmt.Errorf("execute deployment operation: %w", err)
		}
	case currentCommit:

		if err := engine.Commit(ctx, record.OperationID, record.Generation); err != nil {
			return fmt.Errorf("execute deployment operation: %w", err)
		}
	case currentRecover:

		recoveryContext, recoveryCancel := context.WithTimeout(signalContext, time.Duration(record.RecoveryTimeoutSeconds)*time.Second)
		defer recoveryCancel()
		if err := engine.Recover(recoveryContext, record.OperationID, record.Generation, "explicit exact-operation recovery requested"); err != nil {
			return fmt.Errorf("execute deployment operation: %w", err)
		}
	default:
		return fmt.Errorf("unsupported deployment operation mode %s", mode)
	}
	return writeDeployOperationStatus(ctx, engine)
}

func writeDeployOperationStatus(ctx context.Context, engine deployoperation.Engine) (resultErr error) {
	defer func() {
		if resultErr != nil {
			engine.Log.WarnContext(ctx, "deployment operation status read failed")
		}
	}()
	record, err := engine.Store.Read(ctx)
	if err != nil {
		return fmt.Errorf("execute deployment operation: %w", err)
	}
	status := deployOperationStatus{Record: record, MutationReady: record.MutationReady(engine.Store.Clock.Now())}
	if status.MutationReady {
		if err := record.Watch.Verify(ctx); err != nil {
			status.MutationReady = false
			status.WatchError = err.Error()
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(status); err != nil {
		return fmt.Errorf("write deployment status: %w", err)
	}
	return nil
}

func armDeployOperation(ctx context.Context, cfg *config.Config, engine deployoperation.Engine, args []string) (resultErr error) {
	defer func() {
		if resultErr != nil {
			engine.Log.WarnContext(ctx, "deployment operation arm rejected")
		}
	}()
	if len(args) != 2 {
		return fmt.Errorf("arm requires one manifest path")
	}
	manifest, err := deployoperation.ReadManifest(args[1])
	if err != nil {
		return fmt.Errorf("execute deployment operation: %w", err)
	}
	if manifest.VMID != cfg.MwanVMID {
		return fmt.Errorf("deployment manifest VMID differs from configured gateway")
	}
	if err := engine.Arm(ctx, manifest); err != nil {
		return fmt.Errorf("execute deployment operation: %w", err)
	}
	return writeDeployOperationStatus(ctx, engine)
}

func validateExistingOperationArguments(mode operationMode, args []string) error {
	count := 3
	if mode == operationLease {
		count = 5
	}
	if mode == operationRelease {
		count = 4
	}
	if len(args) != count {
		return fmt.Errorf("deployment operation %s requires %d arguments", mode, count-1)
	}
	return nil
}
