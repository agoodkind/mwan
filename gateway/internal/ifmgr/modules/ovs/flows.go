package ovs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"antrea.io/libOpenflow/openflow15"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

var errDuplicateFlow = errors.New("OpenFlow rules have identical tables, priorities, and matches")

func compileFlows(
	ctx context.Context,
	rules []ovsconfig.Flow,
	numbers map[string]uint32,
) ([]*openflow15.FlowMod, error) {
	if _, err := ovsconfig.RuleCookies(rules); err != nil {
		slog.ErrorContext(ctx, "OpenFlow rule identification failed", "err", err)

		return nil, fmt.Errorf("identify configured OpenFlow rules: %w", err)
	}

	flows := make([]*openflow15.FlowMod, 0, len(rules)+1)
	removeOwned := openflow15.NewFlowMod()
	removeOwned.Command = openflow15.FC_DELETE
	removeOwned.TableId = openflow15.OFPTT_ALL
	removeOwned.Cookie = mwanFlowCookie
	removeOwned.CookieMask = mwanFlowMask
	flows = append(flows, removeOwned)

	seen := make(map[flowIdentity]string, len(rules))
	for _, rule := range rules {
		flow, err := marshalFlow(ctx, rule, numbers)
		if err != nil {
			slog.ErrorContext(
				ctx,
				"OpenFlow rule compilation failed",
				"rule",
				rule.Name,
				"err",
				err,
			)

			return nil, fmt.Errorf("compile OpenFlow rule %q: %w", rule.Name, err)
		}

		key, err := ruleKey(0, flow.TableId, flow.Priority, flow.Match, nil)
		if err != nil {
			return nil, fmt.Errorf("encode match for OpenFlow rule %q: %w", rule.Name, err)
		}

		if original, found := seen[key.identity]; found {
			return nil, fmt.Errorf("%w: %q and %q", errDuplicateFlow, original, rule.Name)
		}

		seen[key.identity] = rule.Name
		flows = append(flows, flow)
	}

	return flows, nil
}
