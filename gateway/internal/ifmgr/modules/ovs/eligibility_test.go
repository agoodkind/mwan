package ovs

import (
	"errors"
	"slices"
	"testing"

	"goodkind.io/mwan/gateway/internal/ovsconfig"
	"goodkind.io/mwan/gateway/internal/wanstate"
)

func TestConditionsFollowHealthWithoutChangingConfiguredRules(t *testing.T) {
	condition := &ovsconfig.Condition{HealthyConnection: "wan-a"}
	config := ovsconfig.Config{
		OpenFlow: &ovsconfig.OpenFlowConfig{
			Flows: []ovsconfig.Flow{
				{Name: "always", Priority: 1},
				{Name: "conditional", Priority: 2, Condition: condition},
				{
					Name:      "outage",
					Priority:  3,
					Condition: &ovsconfig.Condition{UnhealthyConnection: "wan-a"},
				},
			},
			Groups: []ovsconfig.Group{
				{
					ID:   1,
					Type: ovsconfig.GroupSelect,
					Buckets: []ovsconfig.Bucket{
						{
							ID:        0,
							Actions:   []ovsconfig.Action{{Name: "output", Argument: "wan-a"}},
							Condition: condition,
						},
						{ID: 1, Actions: []ovsconfig.Action{{Name: "output", Argument: "wan-b"}}},
						{
							ID:        2,
							Actions:   []ovsconfig.Action{{Name: "output", Argument: "wan-b"}},
							Condition: &ovsconfig.Condition{UnhealthyConnection: "wan-a"},
						},
					},
				},
			},
		},
	}

	for _, verdict := range []wanstate.Health{
		wanstate.HealthUnknown,
		wanstate.HealthHealthy,
		wanstate.HealthUnhealthy,
		wanstate.HealthHealthy,
	} {
		flows, groups, err := compileOpenFlow(
			t.Context(),
			config.OpenFlow,
			map[string]uint32{"wan-a": 3, "wan-b": 5},
			map[string]wanstate.MemberHealth{
				"wan-a": {Verdict: verdict},
			},
			installedEntries{},
		)
		if err != nil {
			t.Fatal(err)
		}

		wantFlows := []uint16{1}
		wantBuckets := []uint32{1}
		if verdict == wanstate.HealthHealthy {
			wantFlows = append(wantFlows, 2)
			wantBuckets = []uint32{0, 1}
		} else if verdict == wanstate.HealthUnhealthy {
			wantFlows = append(wantFlows, 3)
			wantBuckets = []uint32{1, 2}
		}

		var priorities []uint16

		for _, flow := range flows[1:] {
			priorities = append(priorities, flow.Priority)
		}

		var buckets []uint32

		for _, bucket := range groups[0].Buckets {
			buckets = append(buckets, bucket.BucketId)
		}

		if !slices.Equal(priorities, wantFlows) || !slices.Equal(buckets, wantBuckets) {
			t.Fatalf(
				"health %s compiled flow priorities %v and bucket IDs %v, want %v and %v",
				verdict,
				priorities,
				buckets,
				wantFlows,
				wantBuckets,
			)
		}

		if len(config.OpenFlow.Flows) != 3 || len(config.OpenFlow.Groups[0].Buckets) != 3 ||
			config.OpenFlow.Flows[1].Condition != condition || config.OpenFlow.Groups[0].Buckets[0].Condition != condition {
			t.Fatal("health resolution changed its source configuration")
		}
	}
}

func TestCompileOpenFlowRejectsInvalidInactiveEntries(t *testing.T) {
	condition := &ovsconfig.Condition{HealthyConnection: "uplink"}
	ports := map[string]uint32{"wan": 9}

	for _, test := range []struct {
		name   string
		config ovsconfig.OpenFlowConfig
		want   error
	}{
		{
			name: "invalid instruction",
			config: ovsconfig.OpenFlowConfig{
				Flows: []ovsconfig.Flow{
					{
						Name:      "invalid",
						Condition: condition,
						Actions:   []ovsconfig.Action{{Name: "unknown_action"}},
					},
				},
			},
			want: errExpression,
		},
		{
			name: "unavailable bucket port",
			config: ovsconfig.OpenFlowConfig{
				Groups: []ovsconfig.Group{
					{
						ID:   1,
						Type: ovsconfig.GroupSelect,
						Buckets: []ovsconfig.Bucket{
							{
								ID:        1,
								Actions:   []ovsconfig.Action{{Name: "output", Argument: "missing"}},
								Condition: condition,
							},
						},
					},
				},
			},
			want: errExpression,
		},
		{
			name: "duplicate match",
			config: ovsconfig.OpenFlowConfig{
				Flows: []ovsconfig.Flow{
					{
						Name:     "first",
						Match:    []ovsconfig.Match{{Field: "in_port", Value: "wan"}},
						Priority: 20,
					},
					{
						Name:      "second",
						Match:     []ovsconfig.Match{{Field: "in_port", Value: "wan"}},
						Priority:  20,
						Condition: condition,
					},
				},
			},
			want: errDuplicateFlow,
		},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				_, _, err := compileOpenFlow(
					t.Context(),
					&test.config,
					ports,
					nil,
					installedEntries{},
				)
				if !errors.Is(err, test.want) {
					t.Fatalf("compileOpenFlow returned %v, want %v", err, test.want)
				}
			},
		)
	}
}

func TestUnknownHealthPreservesOnlyInstalledMembers(t *testing.T) {
	unknown := &ovsconfig.Condition{UnhealthyConnection: "unknown"}
	up := &ovsconfig.Condition{HealthyConnection: "up"}
	down := &ovsconfig.Condition{HealthyConnection: "down"}
	config := ovsconfig.OpenFlowConfig{
		Flows: []ovsconfig.Flow{
			{Name: "retained", Priority: 1, Condition: unknown},
			{Name: "absent", Priority: 2, Condition: unknown},
			{Name: "foreign", Priority: 3, Condition: unknown},
			{Name: "activate", Priority: 4, Condition: up},
			{Name: "remove", Priority: 5, Condition: down},
		},
		Groups: []ovsconfig.Group{
			{
				ID:   1,
				Type: ovsconfig.GroupSelect,
				Buckets: []ovsconfig.Bucket{
					{ID: 1, Condition: unknown},
					{ID: 2, Condition: unknown},
					{ID: 4, Condition: up},
					{ID: 5, Condition: down},
				},
			},
			{
				ID:      2,
				Type:    ovsconfig.GroupSelect,
				Buckets: []ovsconfig.Bucket{{ID: 2, Condition: unknown}},
			},
		},
	}
	installed := installedEntries{
		flows: map[flowIdentity]flowContents{
			{priority: 1}: {cookie: mwanFlowCookie, instructions: "previous instructions"},
			{priority: 3}: {cookie: 42},
			{priority: 5}: {cookie: mwanFlowCookie},
		},
		groups: map[uint32]installedGroup{
			groupNamespace | 1: {buckets: map[uint32]bool{1: true, 5: true}},
			groupNamespace | 2: {buckets: map[uint32]bool{2: true}},
		},
	}
	health := map[string]wanstate.MemberHealth{
		"up":   {Verdict: wanstate.HealthHealthy},
		"down": {Verdict: wanstate.HealthUnhealthy},
	}
	flows, groups, err := compileOpenFlow(t.Context(), &config, nil, health, installed)
	if err != nil {
		t.Fatal(err)
	}

	var priorities []uint16

	for _, flow := range flows[1:] {
		priorities = append(priorities, flow.Priority)
	}

	if !slices.Equal(priorities, []uint16{1, 4}) {
		t.Fatalf("active priorities = %v, want [1 4]", priorities)
	}

	wanted := [][]uint32{{1, 4}, {2}}

	for index, group := range groups {
		var ids []uint32

		for _, bucket := range group.Buckets {
			ids = append(ids, bucket.BucketId)
		}

		if !slices.Equal(ids, wanted[index]) {
			t.Errorf("group %d buckets = %v, want %v", group.GroupId, ids, wanted[index])
		}
	}
}
