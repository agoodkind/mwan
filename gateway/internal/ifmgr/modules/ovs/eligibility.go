package ovs

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
	flows, err := compileFlows(ctx, config.Flows, ports, health, installed.flows)
	if err != nil {
		return nil, nil, err
	}

	groups, err := compileGroups(ctx, config.Groups, ports)
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

func entryActive(
	condition *ovsconfig.Condition,
	health map[string]wanstate.MemberHealth,
	installed bool,
) bool {
	if condition == nil {
		return true
	}

	verdict := health[condition.HealthyConnection].Verdict
	if verdict == wanstate.HealthHealthy {
		return true
	}

	if verdict == wanstate.HealthUnhealthy {
		return false
	}

	return installed
}
