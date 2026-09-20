package networkd

import (
	"context"
	"fmt"
	"log/slog"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
)

// networkdUnit is the network manager's service, which is asked to reload
// after a write so a changed .network or .netdev takes effect without a
// reboot.
const networkdUnit = "systemd-networkd.service"

// The manager's active state for a running unit, and the job result that
// means a reload ran to completion.
const (
	activeStateActive = "active"
	jobResultDone     = "done"
)

// ReloadIfRunning asks the network manager to reload its unit files when it
// is running, and does nothing when it is not. At boot the daemon runs
// before the manager, so the manager reads the fresh files when it starts;
// after a deploy the daemon restarts while the manager is up, and the
// reload is what makes a changed .network or .netdev take effect. A changed
// .link is not carried by the reload, because udev reads a .link when the
// device appears; the caller logs that case.
func ReloadIfRunning(ctx context.Context) error {
	conn, err := systemddbus.NewSystemConnectionContext(ctx)
	if err != nil {
		slog.ErrorContext(ctx, "networkd: connecting to systemd failed", "err", err)
		return fmt.Errorf("connect to systemd: %w", err)
	}
	defer conn.Close()

	statuses, err := conn.ListUnitsByNamesContext(ctx, []string{networkdUnit})
	if err != nil {
		slog.ErrorContext(ctx, "networkd: reading the unit state failed", "unit", networkdUnit, "err", err)
		return fmt.Errorf("read %s state: %w", networkdUnit, err)
	}
	running := false
	for _, status := range statuses {
		if status.Name == networkdUnit && status.ActiveState == activeStateActive {
			running = true
		}
	}
	if !running {
		slog.InfoContext(ctx, "networkd: not running, so it reads the unit files when it starts", "unit", networkdUnit)
		return nil
	}

	result := make(chan string, 1)
	if _, err := conn.ReloadUnitContext(ctx, networkdUnit, "replace", result); err != nil {
		slog.ErrorContext(ctx, "networkd: reload request failed", "unit", networkdUnit, "err", err)
		return fmt.Errorf("reload %s: %w", networkdUnit, err)
	}
	select {
	case outcome := <-result:
		if outcome != jobResultDone {
			slog.ErrorContext(ctx, "networkd: reload did not complete", "unit", networkdUnit, "result", outcome)
			return fmt.Errorf("reload %s: job result %s", networkdUnit, outcome)
		}
	case <-ctx.Done():
		slog.ErrorContext(ctx, "networkd: reload interrupted", "unit", networkdUnit, "err", ctx.Err())
		return fmt.Errorf("reload %s: %w", networkdUnit, ctx.Err())
	}
	slog.InfoContext(ctx, "networkd: reloaded", "unit", networkdUnit)
	return nil
}
