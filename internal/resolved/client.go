// Package resolved manages per-link static resolver settings through systemd-resolved.
package resolved

import (
	"context"
	"fmt"
	"log/slog"
	"math"

	"github.com/godbus/dbus/v5"
)

const (
	service          = "org.freedesktop.resolve1"
	managerInterface = service + ".Manager"
	linkInterface    = service + ".Link"
)

// DNS preserves the complete resolved DNSEx server tuple.
type DNS struct {
	Family     int32  `json:"family"`
	Address    []byte `json:"address"`
	Port       uint16 `json:"port"`
	ServerName string `json:"server_name"`
}

// Domain preserves a search or routing domain.
type Domain struct {
	Name        string `json:"name"`
	RoutingOnly bool   `json:"routing_only"`
}

type client struct {
	connection *dbus.Conn
	manager    dbus.BusObject
}

func openClient() (*client, error) {
	connection, err := dbus.SystemBusPrivate()
	if err != nil {
		return nil, failure("connect system bus", err)
	}
	if err := connection.Auth(nil); err != nil {
		_ = connection.Close()
		return nil, failure("authenticate system bus", err)
	}
	if err := connection.Hello(); err != nil {
		_ = connection.Close()
		return nil, failure("register system bus connection", err)
	}
	return &client{connection: connection, manager: connection.Object(service, dbus.ObjectPath("/org/freedesktop/resolve1"))}, nil
}

func (c *client) property(ctx context.Context, index int, name string) (dbus.Variant, error) {
	if index <= 0 || index > math.MaxInt32 {
		return dbus.Variant{}, fmt.Errorf("invalid resolver link index %d", index)
	}
	var path dbus.ObjectPath
	if err := c.manager.CallWithContext(ctx, managerInterface+".GetLink", 0, int32(index)).Store(&path); err != nil {
		return dbus.Variant{}, failure("get resolved link", err)
	}
	var value dbus.Variant
	if err := c.connection.Object(service, path).CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, linkInterface, name).Store(&value); err != nil {
		return dbus.Variant{}, failure("read resolved "+name, err)
	}
	return value, nil
}

func (c *client) dns(ctx context.Context, index int) ([]DNS, error) {
	values := []DNS{}
	value, err := c.property(ctx, index, "DNSEx")
	if err != nil {
		return nil, err
	}
	if err := value.Store(&values); err != nil {
		return nil, failure("decode resolved DNS", err)
	}
	return values, nil
}

func (c *client) domains(ctx context.Context, index int) ([]Domain, error) {
	values := []Domain{}
	value, err := c.property(ctx, index, "Domains")
	if err != nil {
		return nil, err
	}
	if err := value.Store(&values); err != nil {
		return nil, failure("decode resolved domains", err)
	}
	return values, nil
}

func (c *client) setDNS(ctx context.Context, index int, values []DNS) error {
	if index <= 0 || index > math.MaxInt32 {
		return fmt.Errorf("invalid resolver link index %d", index)
	}
	if values == nil {
		values = []DNS{}
	}
	if err := c.manager.CallWithContext(ctx, managerInterface+".SetLinkDNSEx", 0, int32(index), values).Err; err != nil {
		return failure("set resolved DNS", err)
	}
	return nil
}

func (c *client) setDomains(ctx context.Context, index int, values []Domain) error {
	if index <= 0 || index > math.MaxInt32 {
		return fmt.Errorf("invalid resolver link index %d", index)
	}
	if values == nil {
		values = []Domain{}
	}
	if err := c.manager.CallWithContext(ctx, managerInterface+".SetLinkDomains", 0, int32(index), values).Err; err != nil {
		return failure("set resolved domains", err)
	}
	return nil
}

func failure(operation string, err error) error {
	slog.Warn("resolver operation failed", "operation", operation, "result", "failed")
	return fmt.Errorf("%s: %w", operation, err)
}
