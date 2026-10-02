package deployoperation

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

type WatchIdentity struct {
	Unit         string `json:"unit"`
	InvocationID string `json:"invocation_id"`
	PID          uint32 `json:"pid"`
}

func StartWatch(ctx context.Context, record Record, runtimePath string) error {
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve deploy watch executable: %w", err)
	}
	if !filepath.IsAbs(executable) || !filepath.IsAbs(runtimePath) {
		return fmt.Errorf("deploy watch requires absolute executable and explicit runtime configuration paths")
	}
	connection, err := systemddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		return fmt.Errorf("connect to systemd for deploy watch startup: %w", err)
	}
	defer connection.Close()
	remaining := time.Until(record.Deadline.Add(time.Duration(record.RecoveryTimeoutSeconds) * time.Second))
	if remaining <= 0 {
		return fmt.Errorf("deploy watch execution deadline has expired")
	}
	result := make(chan string, 1)
	properties := []systemddbus.Property{
		systemddbus.PropExecStart([]string{executable, "deploy-gate", "watch", record.OperationID, record.Generation, "--config", runtimePath}, false),
		{Name: "Type", Value: dbus.MakeVariant("exec")},
		{Name: "RuntimeMaxUSec", Value: dbus.MakeVariant(uint64(remaining.Microseconds()))},
		{Name: "KillMode", Value: dbus.MakeVariant("control-group")},
	}
	if _, err := connection.StartTransientUnitContext(ctx, record.WatchUnit, "fail", properties, result); err != nil {
		return fmt.Errorf("start exact deploy watch unit: %w", err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case outcome := <-result:
		if outcome != "done" {
			return fmt.Errorf("deploy watch startup job returned %s", outcome)
		}
		return nil
	}
}

func ReadWatch(ctx context.Context, unit string) (WatchIdentity, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return WatchIdentity{}, fmt.Errorf("deploy watch observation requires a context deadline")
	}
	connection, err := systemddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		slog.WarnContext(ctx, "deploy watch systemd connection failed")
		return WatchIdentity{}, fmt.Errorf("connect to deploy watch systemd manager: %w", err)
	}
	defer connection.Close()
	active, err := connection.GetUnitPropertyContext(ctx, unit, "ActiveState")
	if err != nil {
		return WatchIdentity{}, fmt.Errorf("read deploy watch active state: %w", err)
	}
	var state string
	if err := active.Value.Store(&state); err != nil {
		return WatchIdentity{}, fmt.Errorf("decode deploy watch active state: %w", err)
	}
	if state != "active" {
		return WatchIdentity{}, fmt.Errorf("deploy watch unit is %s", state)
	}
	invocation, err := connection.GetUnitPropertyContext(ctx, unit, "InvocationID")
	if err != nil {
		return WatchIdentity{}, fmt.Errorf("read deploy watch invocation: %w", err)
	}
	var invocationID []byte
	if err := invocation.Value.Store(&invocationID); err != nil {
		return WatchIdentity{}, fmt.Errorf("decode deploy watch invocation: %w", err)
	}
	pidProperty, err := connection.GetUnitTypePropertyContext(ctx, unit, "Service", "MainPID")
	if err != nil {
		return WatchIdentity{}, fmt.Errorf("read deploy watch PID: %w", err)
	}
	var pid uint32
	if err := pidProperty.Value.Store(&pid); err != nil {
		return WatchIdentity{}, fmt.Errorf("decode deploy watch PID: %w", err)
	}
	if pid == 0 || len(invocationID) != 16 {
		return WatchIdentity{}, fmt.Errorf("deploy watch lacks a running PID or invocation identity")
	}
	return WatchIdentity{Unit: unit, InvocationID: hex.EncodeToString(invocationID), PID: pid}, nil
}

func (identity WatchIdentity) Verify(ctx context.Context) error {
	observed, err := ReadWatch(ctx, identity.Unit)
	if err != nil {
		return err
	}
	if observed != identity {
		return fmt.Errorf("deploy watch invocation or PID has changed")
	}
	return nil
}
