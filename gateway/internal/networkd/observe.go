package networkd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/godbus/dbus/v5"
)

// Address records networkd's acquisition source for a live address.
type Address struct {
	Prefix       netip.Prefix
	ConfigSource string
	ConfigState  string
}

type describedAddress struct {
	Family       int    `json:"Family"`
	Address      []byte `json:"Address"`
	PrefixLength int    `json:"PrefixLength"`
	ConfigSource string `json:"ConfigSource"`
	ConfigState  string `json:"ConfigState"`
}

type linkDescription struct {
	AdministrativeState string             `json:"AdministrativeState"`
	Addresses           []describedAddress `json:"Addresses"`
}

// AdministrativeState observes networkd without activating an inactive service.
func AdministrativeState(ctx context.Context, name string) (state string, failure error) {
	link, err := describeLink(ctx, name)
	if err != nil {
		return "", err
	}
	return link.AdministrativeState, nil
}

// Addresses observes acquisition sources without activating networkd.
func Addresses(ctx context.Context, name string) (result []Address, failure error) {
	defer func() {
		if failure != nil {
			slog.WarnContext(ctx, "networkd address observation failed", "interface", name, "error", failure)
		}
	}()
	link, err := describeLink(ctx, name)
	if err != nil {
		return nil, err
	}
	for _, address := range link.Addresses {
		ip, valid := netip.AddrFromSlice(address.Address)
		if !valid || (address.Family != 2 && address.Family != 10) || (address.Family == 2 && !ip.Is4()) || (address.Family == 10 && !ip.Is6()) {
			return nil, fmt.Errorf("networkd link %s has an invalid address", name)
		}
		prefix := netip.PrefixFrom(ip, address.PrefixLength)
		if !prefix.IsValid() {
			return nil, fmt.Errorf("networkd link %s has an invalid address prefix", name)
		}
		result = append(result, Address{Prefix: prefix, ConfigSource: address.ConfigSource, ConfigState: address.ConfigState})
	}
	return result, nil
}

func describeLink(ctx context.Context, name string) (result linkDescription, failure error) {
	defer func() {
		if failure != nil {
			slog.WarnContext(ctx, "networkd link observation failed", "interface", name, "error", failure)
		}
	}()
	bus, err := dbus.ConnectSystemBus(dbus.WithContext(ctx))
	if err != nil {
		return result, fmt.Errorf("connect to networkd bus: %w", err)
	}
	defer bus.Close()
	manager := bus.Object("org.freedesktop.network1", "/org/freedesktop/network1")
	var index int32
	var path dbus.ObjectPath
	if err := manager.CallWithContext(ctx, "org.freedesktop.network1.Manager.GetLinkByName", dbus.FlagNoAutoStart, name).Store(&index, &path); err != nil {
		return result, fmt.Errorf("find networkd link %s: %w", name, err)
	}
	var data string
	if err := bus.Object("org.freedesktop.network1", path).CallWithContext(ctx, "org.freedesktop.network1.Link.Describe", dbus.FlagNoAutoStart).Store(&data); err != nil {
		return result, fmt.Errorf("describe networkd link %s: %w", name, err)
	}
	if err := json.Unmarshal([]byte(data), &result); err != nil {
		return result, fmt.Errorf("decode networkd link %s: %w", name, err)
	}
	if result.AdministrativeState == "" {
		return result, fmt.Errorf("networkd link %s has no administrative state", name)
	}
	return result, nil
}
