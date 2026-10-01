package networkd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/godbus/dbus/v5"
)

// AdministrativeState observes networkd without activating an inactive service.
func AdministrativeState(ctx context.Context, name string) (state string, failure error) {
	defer func() {
		if failure != nil {
			slog.WarnContext(ctx, "networkd administrative state read failed", "interface", name, "error", failure)
		}
	}()
	bus, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return "", fmt.Errorf("connect to networkd bus: %w", err)
	}
	defer bus.Close()
	manager := bus.Object("org.freedesktop.network1", "/org/freedesktop/network1")
	var index int32
	var path dbus.ObjectPath
	if err := manager.CallWithContext(ctx, "org.freedesktop.network1.Manager.GetLinkByName", dbus.FlagNoAutoStart, name).Store(&index, &path); err != nil {
		return "", fmt.Errorf("find networkd link %s: %w", name, err)
	}
	var data string
	if err := bus.Object("org.freedesktop.network1", path).CallWithContext(ctx, "org.freedesktop.network1.Link.Describe", dbus.FlagNoAutoStart).Store(&data); err != nil {
		return "", fmt.Errorf("describe networkd link %s: %w", name, err)
	}
	var link struct {
		AdministrativeState string `json:"AdministrativeState"`
	}
	if err := json.Unmarshal([]byte(data), &link); err != nil {
		return "", fmt.Errorf("decode networkd link %s: %w", name, err)
	}
	if link.AdministrativeState == "" {
		return "", fmt.Errorf("networkd link %s has no administrative state", name)
	}
	return link.AdministrativeState, nil
}
