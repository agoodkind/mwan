//go:build !linux

package ovs

// Bridge setup requires Linux devlink to verify OVS-DOCA multiport readiness.
// On other systems this check returns the platform error before setup can
// accept a configuration that requests DOCA offload.

import (
	"context"
	"errors"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

var errMultiportPlatform = errors.New("OVS-DOCA multiport readiness requires Linux devlink")

func requireMultiport(context.Context, []ovsconfig.Port) error { return errMultiportPlatform }
