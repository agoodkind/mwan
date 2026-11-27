package ovs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/mwan/gateway/internal/clock"
	"goodkind.io/mwan/gateway/internal/config"
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
	clock     clock.Clock
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
		clock:      clock.Real{},
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
		var snapshot wanstate.Snapshot
		bind := hasLeaseValues(settings.OpenFlow)
		if m.Env.LiveState != nil && (bind || hasConditions(settings)) {
			snapshot = m.Env.LiveState.Snapshot()
			health = snapshot.Health
		}

		if bind {
			values := leaseValues{connections: snapshot.Connections, now: m.clock.Now()}
			var rules ovsconfig.OpenFlowConfig
			rules, err = values.openFlow(ctx, *settings.OpenFlow)
			settings.OpenFlow = &rules
		}

		if err == nil {
			err = Setup(ctx, settings, health)
		}
	}

	m.lastError = err
	if err != nil {
		log.ErrorContext(ctx, "OVS reconciliation failed", "err", err)

		return fmt.Errorf("reconcile OVS: %w", err)
	}

	return nil
}

func hasLeaseValues(rules *ovsconfig.OpenFlowConfig) bool {
	if rules == nil {
		return false
	}

	for _, mapping := range rules.Mappings {
		if mapping.ExternalSource == config.PrefixDelegated {
			return true
		}
	}

	for _, flow := range rules.Flows {
		for _, entry := range flow.Match {
			if strings.Contains(entry.Value, "${") {
				return true
			}
		}

		if actionsUseLeases(flow.Actions) {
			return true
		}
	}

	for _, group := range rules.Groups {
		for _, bucket := range group.Buckets {
			if actionsUseLeases(bucket.Actions) {
				return true
			}
		}
	}

	return false
}

func actionsUseLeases(actions []ovsconfig.Action) bool {
	for _, action := range actions {
		if strings.Contains(action.Argument, "${") {
			return true
		}
	}

	return false
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
