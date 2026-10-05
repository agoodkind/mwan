//go:build !linux

package netif

import (
	"context"
	"fmt"
	"log/slog"
)

func StartRuleMonitor(context.Context, *slog.Logger, func(RuleEvent)) error {
	return fmt.Errorf("policy-rule monitoring requires Linux")
}
