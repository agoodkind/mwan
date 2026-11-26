package ovs

import (
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	"goodkind.io/mwan/gateway/internal/ifmgr"
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
			},
			Groups: []ovsconfig.Group{
				{
					ID:   1,
					Type: ovsconfig.GroupSelect,
					Buckets: []ovsconfig.Bucket{
						{ID: 0, Output: "wan-a", Condition: condition},
						{ID: 1, Output: "wan-b"},
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
		)
		if err != nil {
			t.Fatal(err)
		}

		wantFlows := []uint16{1}
		wantBuckets := []uint32{1}
		if verdict == wanstate.HealthHealthy {
			wantFlows = append(wantFlows, 2)
			wantBuckets = []uint32{0, 1}
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

		if len(config.OpenFlow.Flows) != 2 || len(config.OpenFlow.Groups[0].Buckets) != 2 ||
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
						Name:           "invalid",
						Condition:      condition,
						RawInstruction: [][]byte{make([]byte, 8)},
					},
				},
			},
			want: errRawInstruction,
		},
		{
			name: "unavailable bucket port",
			config: ovsconfig.OpenFlowConfig{
				Groups: []ovsconfig.Group{
					{
						ID:      1,
						Type:    ovsconfig.GroupSelect,
						Buckets: []ovsconfig.Bucket{{ID: 1, Output: "missing", Condition: condition}},
					},
				},
			},
			want: errUnknownFlowPort,
		},
		{
			name: "duplicate match",
			config: ovsconfig.OpenFlowConfig{
				Flows: []ovsconfig.Flow{
					{Name: "first", InPort: "wan", Priority: 20},
					{Name: "second", InPort: "wan", Priority: 20, Condition: condition},
				},
			},
			want: errDuplicateFlow,
		},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				_, _, err := compileOpenFlow(t.Context(), &test.config, ports, nil)
				if !errors.Is(err, test.want) {
					t.Fatalf("compileOpenFlow returned %v, want %v", err, test.want)
				}
			},
		)
	}
}

func TestConditionRequiresRuntimeEvaluation(t *testing.T) {
	config := ovsconfig.Config{
		OpenFlow: &ovsconfig.OpenFlowConfig{
			Flows: []ovsconfig.Flow{
				{
					Condition: &ovsconfig.Condition{HealthyConnection: "wan-a"},
				},
			},
		},
	}
	if err := Setup(t.Context(), config, nil); !errors.Is(err, errHealthStore) {
		t.Fatalf("Setup error = %v, want missing health snapshot", err)
	}

	module, err := newModule(Loader(func() (ovsconfig.Config, error) { return config, nil }))
	if err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := module.Init(t.Context(), &ifmgr.Env{Log: logger}); err != nil {
		t.Fatal(err)
	}

	if err := module.Reconcile(t.Context(), logger); !errors.Is(err, errHealthStore) {
		t.Fatalf("Reconcile error = %v, want missing health store", err)
	}
}
