package policyrules_test

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/ifmgr/modules/policyrules"
	"goodkind.io/mwan/internal/netif"
)

func TestInitWrapsRuleValidationErrors(t *testing.T) {
	const (
		validPriority = 10
		validTableID  = 100
	)

	for _, test := range []struct {
		name     string
		priority int
		tableID  int
		message  string
		cause    string
	}{
		{
			name:     "priority",
			priority: 0,
			tableID:  validTableID,
			message:  "policy_rules[1]: priority must be > 0",
			cause:    "priority must be > 0",
		},
		{
			name:     "table",
			priority: validPriority,
			tableID:  0,
			message:  "policy_rules[1]: table_id must be > 0 (rule prio=10)",
			cause:    "table_id must be > 0",
		},
	} {
		t.Run(
			test.name,
			func(t *testing.T) {
				rules := make([]netif.DesiredRule, 2)
				rules[0].Priority = validPriority
				rules[0].TableID = validTableID
				rules[1].Priority = test.priority
				rules[1].TableID = test.tableID
				module, err := policyrules.New(policyrules.Config{Rules: rules})
				if err != nil {
					t.Fatal(err)
				}

				var output bytes.Buffer

				environment := new(ifmgr.Env)
				environment.Log = slog.New(slog.NewTextHandler(&output, nil))
				failure := module.Init(t.Context(), environment)
				if failure == nil || failure.Error() != test.message {
					t.Fatalf("validation error = %v; want %s", failure, test.message)
				}

				cause := errors.Unwrap(failure)
				if cause == nil || cause.Error() != test.cause {
					t.Fatalf("validation cause = %v; want %s", cause, test.cause)
				}

				warning := output.String()
				if !strings.Contains(warning, "level=WARN") ||
					!strings.Contains(warning, "rule_index=1") ||
					!strings.Contains(warning, test.cause) {
					t.Fatalf("validation warning lacks the rule index or cause: %s", warning)
				}
			},
		)
	}
}
