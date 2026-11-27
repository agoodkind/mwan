package ovs

// ifmgr invokes Setup after the OVS module loads settings and binds any lease
// values. One deadline covers both the OVSDB bridge reconciliation and the
// optional OpenFlow update.

import (
	"context"
	"time"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

// Setup configures an OVS bridge, its ports, and its OpenFlow rules.
//
// After ovs-vswitchd assigns ports, Setup replaces mwan's rules and groups
// in one OpenFlow bundle. Other controllers retain their rules.
//
// A bucket gated by healthy-connection "wan-a" stays out of its group while
// the probe reports that the connection is down.
//
//	health := map[string]wanstate.MemberHealth{"wan-a": {Verdict: wanstate.HealthUnhealthy}}
//	if err := ovs.Setup(ctx, settings, health); err != nil {
//		return fmt.Errorf("configure bridge %s: %w", settings.Bridge, err)
//	}
func Setup(
	parent context.Context,
	config ovsconfig.Config,
	health map[string]wanstate.MemberHealth,
) error {
	if health == nil && hasConditions(config) {
		return errHealthStore
	}

	ctx, cancel := context.WithTimeout(parent, time.Duration(config.TimeoutMillis)*time.Millisecond)
	defer cancel()
	database, err := connect(ctx, config.Socket)
	if err != nil {
		return err
	}
	defer database.Close()

	if err := reconcileBridge(ctx, database, config); err != nil {
		return err
	}

	if config.OpenFlow == nil {
		return nil
	}

	return installOpenFlow(ctx, database, config, health)
}
