//go:build !linux || !cgo || !nativeoffload

package nativeoffload

import (
	"context"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/ifmgr"
)

type unavailableModule struct {
	ifmgr.BaseModule
	cfg Config
}

func New(configuration ifmgr.ModuleConfig) (ifmgr.Module, error) {
	var settings Config
	if configuration != nil {
		var ok bool
		settings, ok = configuration.(Config)
		if !ok {
			return nil, fmt.Errorf("nativeoffload: invalid configuration type %T", configuration)
		}
	}
	return &unavailableModule{BaseModule: ifmgr.NewBaseModule("nativeoffload"), cfg: settings}, nil
}

func (module *unavailableModule) Init(_ context.Context, _ *ifmgr.Env) error {
	if err := ValidateSupport(module.cfg); err != nil {
		return err
	}
	return ifmgr.ErrModuleDisabled
}

func (*unavailableModule) Reconcile(context.Context, *slog.Logger) error { return nil }

func init() { ifmgr.Register("nativeoffload", New) }
