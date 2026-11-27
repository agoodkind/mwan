package ovs

import (
	"errors"
	"io"
	"log/slog"
	"testing"

	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

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
