// Package policyrules implements the ifmgr policy-rules module: keeps a
// configured list of `ip rule` entries present in the kernel. Each rule
// is an (priority, family, selector, table) tuple. Foreign rules at
// unrelated priorities are not touched.
//
// Registers as "policy_rules". Selected by the oob role today;
// reusable for any role that needs static policy routing.
package policyrules

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/netif"
)

var (
	errInvalidPriority = errors.New("priority must be > 0")
	errInvalidTableID  = errors.New("table_id must be > 0")
)

// Module owns the policy-rules state.
type Module struct {
	ifmgr.BaseModule

	cfg Config
}

// Config is the parsed [ifmgr.modules.policy_rules] sub-config. Note the
// table is named "rule" in TOML to match the [[ifmgr.modules.policy_rules.rule]]
// array-of-tables idiom; here we accept the parsed list of rules.
type Config struct {
	Rules []netif.DesiredRule
}

// ModuleConfigName returns the registry key for this module's config block.
func (Config) ModuleConfigName() string { return "policy_rules" }

// Init implements ifmgr.Module. Sanity-checks each rule.
func (m *Module) Init(ctx context.Context, env *ifmgr.Env) error {
	log := m.InitBase(env, "module", "policy_rules")
	log.InfoContext(ctx, "policy_rules: Init", "rule_count", len(m.cfg.Rules))

	for ruleIndex, rule := range m.cfg.Rules {
		if rule.Priority <= 0 {
			log.WarnContext(
				ctx,
				"policy_rules: invalid rule",
				"rule_index",
				ruleIndex,
				"err",
				errInvalidPriority,
			)

			return fmt.Errorf("policy_rules[%d]: %w", ruleIndex, errInvalidPriority)
		}

		if rule.TableID <= 0 {
			log.WarnContext(
				ctx,
				"policy_rules: invalid rule",
				"rule_index",
				ruleIndex,
				"err",
				errInvalidTableID,
			)

			return fmt.Errorf(
				"policy_rules[%d]: %w (rule prio=%d)",
				ruleIndex,
				errInvalidTableID,
				rule.Priority,
			)
		}
	}

	return nil
}

// Reconcile implements ifmgr.Module.
func (m *Module) Reconcile(ctx context.Context, log *slog.Logger) error {
	if err := netif.ReconcileRules(ctx, log, m.cfg.Rules); err != nil {
		log.WarnContext(ctx, "policy_rules: ReconcileRules failed", "err", err)
		return fmt.Errorf("reconcile policy rules: %w", err)
	}

	return nil
}

// New is the Constructor.
func New(cfg ifmgr.ModuleConfig) (ifmgr.Module, error) {
	c := Config{
		Rules: nil,
	}
	if cfg != nil {
		typedConfig, ok := cfg.(Config)
		if !ok {
			return nil, fmt.Errorf("policy_rules: invalid config type %T", cfg)
		}

		c = typedConfig
	}

	return &Module{
		BaseModule: ifmgr.NewBaseModule("policy_rules"),
		cfg:        c,
	}, nil
}

func init() { ifmgr.Register("policy_rules", New) }
