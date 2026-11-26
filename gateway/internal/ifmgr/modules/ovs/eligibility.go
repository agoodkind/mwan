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
) ([]*openflow15.FlowMod, []*openflow15.GroupMod, error) {
	flows, err := compileFlows(ctx, config.Flows, ports)
	if err != nil {
		return nil, nil, err
	}

	groups, err := compileGroups(ctx, config.Groups, ports)
	if err != nil {
		return nil, nil, err
	}

	active := func(condition *ovsconfig.Condition) bool {
		return condition == nil ||
			health[condition.HealthyConnection].Verdict == wanstate.HealthHealthy
	}

	// flows[0] removes previously installed rules from OVS.
	activeFlows := flows[:1]

	for index, flow := range config.Flows {
		if active(flow.Condition) {
			activeFlows = append(activeFlows, flows[index+1])
		}
	}

	for index, group := range groups {
		encoded := group.Buckets
		group.Buckets = encoded[:0]
		group.BucketArrayLen = 0

		for bucketIndex, bucket := range config.Groups[index].Buckets {
			if active(bucket.Condition) {
				group.AddBucket(encoded[bucketIndex])
			}
		}
	}

	return activeFlows, groups, nil
}
