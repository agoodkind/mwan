package networkd

import (
	"context"
	"fmt"
	"log/slog"

	systemddbus "github.com/coreos/go-systemd/v22/dbus"
	"github.com/godbus/dbus/v5"
)

const networkdUnit = "systemd-networkd.service"

const activeStateActive = "active"

// ReloadIfRunning reloads changed .network and .netdev files when networkd is active.
// Inactive networkd reads the files during startup. Udev applies .link files when devices appear.
// A systemd reload job waits for this daemon's Before ordering and READY notification.
// The manager method waits for networkd's reload without scheduling that job.
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

	bus, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		slog.ErrorContext(ctx, "networkd: connecting to the network manager failed", "err", err)
		return fmt.Errorf("connect to network manager: %w", err)
	}
	defer bus.Close()
	manager := bus.Object("org.freedesktop.network1", "/org/freedesktop/network1")
	if err := manager.CallWithContext(ctx, "org.freedesktop.network1.Manager.Reload", 0).Err; err != nil {
		slog.ErrorContext(ctx, "networkd: reload request failed", "unit", networkdUnit, "err", err)
		return fmt.Errorf("reload %s: %w", networkdUnit, err)
	}
	slog.InfoContext(ctx, "networkd: reloaded", "unit", networkdUnit)
	return nil
}
