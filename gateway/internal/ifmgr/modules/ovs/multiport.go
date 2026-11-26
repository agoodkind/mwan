//go:build !linux

package ovs

import (
	"context"
	"errors"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

var errMultiportPlatform = errors.New("OVS-DOCA multiport readiness requires Linux devlink")

func requireMultiport(context.Context, []ovsconfig.Port) error { return errMultiportPlatform }
