//go:build linux && cgo && nativeoffload

package nativeoffload

import (
	"context"

	"goodkind.io/mwan/internal/firewall"
)

func (module *Module) inspectPolicy(ctx context.Context) error {
	desired, err := firewall.Compile(module.cfg.Firewall)
	if err != nil {
		return err
	}
	return firewall.InspectNative(ctx, desired)
}
