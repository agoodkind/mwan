package ovs

// OpenFlow compilation uses WAN health and installed state to choose the
// flows and group buckets to install. Consider an entry that requires wan-a
// to be healthy.
//
//   healthy   -> include the entry
//   unhealthy -> omit the entry
//   unknown   -> keep the installed entry if present

import (
	"context"
	"errors"

	"antrea.io/libOpenflow/openflow15"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

var errHealthStore = errors.New("OVS health conditions require a connection health snapshot")

func hasConditions(config ovsconfig.Config) bool {
	if config.OpenFlow == nil {
		return false
	}

	for _, flow := range config.OpenFlow.Flows {
		if flow.Condition != nil {
			return true
		}
	}

	for _, group := range config.OpenFlow.Groups {
		for _, bucket := range group.Buckets {
			if bucket.Condition != nil {
				return true
			}
		}
	}

	return false
}

func compileOpenFlow(
	ctx context.Context,
	config *ovsconfig.OpenFlowConfig,
	ports map[string]uint32,
	health map[string]wanstate.MemberHealth,
	installed installedEntries,
) ([]*openflow15.FlowMod, []*openflow15.GroupMod, error) {
	configured, err := compileFlows(ctx, config.Flows, ports)
	if err != nil {
		return nil, nil, err
	}

	flows, err := activeFlows(config.Flows, configured, health, installed.flows)
	if err != nil {
		return nil, nil, err
	}

	groups, err := compileGroups(ctx, config.Groups, ports)
	if err != nil {
		return nil, nil, err
	}

	flows, err = compileNPT(config, installed, configured, flows, groups)
	if err != nil {
		return nil, nil, err
	}

	for index, group := range groups {
		members := installed.groups[group.GroupId].buckets
		encoded := group.Buckets
		group.Buckets = encoded[:0]
		group.BucketArrayLen = 0

		for bucketIndex, bucket := range config.Groups[index].Buckets {
			if entryActive(bucket.Condition, health, members[bucket.ID]) {
				group.AddBucket(encoded[bucketIndex])
			}
		}
	}

	return flows, groups, nil
}

func activeFlows(
	rules []ovsconfig.Flow,
	configured []*openflow15.FlowMod,
	health map[string]wanstate.MemberHealth,
	installed map[flowIdentity]flowContents,
) ([]*openflow15.FlowMod, error) {
	flows := make([]*openflow15.FlowMod, 1, len(configured))
	flows[0] = configured[0]

	for index, flow := range configured[1:] {
		condition := rules[index].Condition
		if condition == nil {
			flows = append(flows, flow)

			continue
		}

		key, err := ruleKey(0, flow.TableId, flow.Priority, flow.Match, nil)
		if err != nil {
			return nil, err
		}

		entry := installed[key.identity]
		if entryActive(condition, health, entry.cookie&mwanFlowMask == mwanFlowCookie) {
			flows = append(flows, flow)
		}
	}

	return flows, nil
}

func entryActive(
	condition *ovsconfig.Condition,
	health map[string]wanstate.MemberHealth,
	installed bool,
) bool {
	if condition == nil {
		return true
	}

	switch health[condition.ConnectionID()].Verdict {
	case wanstate.HealthHealthy:
		return condition.HealthyConnection != ""
	case wanstate.HealthUnhealthy:
		return condition.UnhealthyConnection != ""
	case wanstate.HealthUnknown:
		return installed
	}

	return installed
}
