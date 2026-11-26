// Package ovs configures Open vSwitch bridges, ports, and OpenFlow rules.
package ovs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

const moduleName = "ovs"

var errLoader = errors.New("OVS requires a network document loader")

// Loader reads OVS settings from network documents.
type Loader func() (ovsconfig.Config, error)

// ModuleConfigName returns the registry key "ovs" for a WAN settings loader.
//
// ifmgr uses the key to select the constructor. It reads the network document
// later, when the module reconciles the bridge.
//
//	loader := ovs.Loader(func() (ovsconfig.Config, error) {
//		return networkload.LoadOVS(networkPath, schemaDir)
//	})
//	fmt.Println(loader.ModuleConfigName()) // ovs
func (Loader) ModuleConfigName() string { return moduleName }

type module struct {
	ifmgr.BaseModule

	load      Loader
	lastError error
}

func newModule(settings ifmgr.ModuleConfig) (*module, error) {
	load, ok := settings.(Loader)
	if !ok || load == nil {
		slog.Error(
			"OVS module received an invalid loader",
			"type",
			fmt.Sprintf("%T", settings),
			"err",
			errLoader,
		)

		return nil, errLoader
	}

	return &module{
		BaseModule: ifmgr.NewBaseModule(moduleName),
		load:       load,
		lastError:  nil,
	}, nil
}

func init() {
	ifmgr.Register(
		moduleName,
		func(settings ifmgr.ModuleConfig) (ifmgr.Module, error) {
			return newModule(settings)
		},
	)
}

func (m *module) Init(_ context.Context, env *ifmgr.Env) error {
	m.InitBase(env, "module", moduleName)

	return nil
}

func (m *module) Reconcile(ctx context.Context, log *slog.Logger) error {
	m.Lock()
	defer m.Unlock()

	settings, err := m.load()
	var health map[string]wanstate.MemberHealth

	if err == nil {
		if m.Env.LiveState != nil && hasConditions(settings) {
			health = m.Env.LiveState.Snapshot().Health
		}

		err = Setup(ctx, settings, health)
	}

	m.lastError = err
	if err != nil {
		log.ErrorContext(ctx, "OVS reconciliation failed", "err", err)

		return fmt.Errorf("reconcile OVS: %w", err)
	}

	return nil
}

func (m *module) EvaluateAlerts(ctx context.Context, _ *slog.Logger, now time.Time) {
	m.Lock()
	defer m.Unlock()
	if m.lastError != nil {
		m.Env.Alerts.NotifyContext(
			ctx,
			now,
			slog.LevelError,
			"ovs_reconcile",
			m.Env.Iface,
			"OVS configuration failed",
			slog.Any("err", m.lastError),
		)

		return
	}

	m.Env.Alerts.ResolveContext(
		ctx,
		now,
		"ovs_reconcile",
		m.Env.Iface,
		"OVS configuration recovered",
	)
}
