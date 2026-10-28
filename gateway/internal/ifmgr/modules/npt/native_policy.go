package npt

import (
	"context"
	"fmt"

	"goodkind.io/mwan/internal/ifmgr/modules/npt/bpf"
)

func (m *Module) inspectNativePacketPolicy(ctx context.Context, indices []int) error {
	if m.Env.LiveState == nil {
		return fmt.Errorf("native packet policy requires applied NPT state")
	}
	policies, _, _, err := m.interfacePolicies(ctx, m.Env.Log, m.Env.LiveState.Snapshot().NPTPolicies)
	if err != nil {
		return err
	}
	inspector, available := m.translator.(interface {
		InspectNativePolicy(context.Context, []int, []bpf.InterfacePolicy) error
	})
	if !available {
		return fmt.Errorf("NPT translator cannot inspect native packet policy")
	}
	// Avoid the module lock: policy writers acquire the native guard before
	// acquiring the translator lock, and this inspector already owns that guard.
	return inspector.InspectNativePolicy(ctx, indices, policies)
}
