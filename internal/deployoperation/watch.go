package deployoperation

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
)

type WatchIdentity struct {
	Unit         string `json:"unit"`
	InvocationID string `json:"invocation_id"`
	PID          uint32 `json:"pid"`
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
