//go:build linux && cgo

package ovs

// NPTv6 helper tables must avoid tables that configured or foreign rules use
// or reference. This fixture reserves tables 250 through 253 and checks that the
// compiler can reuse table 249, which mwan already owns. It also checks that
// a nested resubmit on a connection bucket specifies its target table.

import (
	"errors"
	"net/netip"
	"testing"

	"goodkind.io/mwan/gateway/internal/config"
	"goodkind.io/mwan/gateway/internal/ovsconfig"
)

func TestConnectionTranslationTables(t *testing.T) {
	mapping := config.NPTv6Translation{
		InternalPrefix: netip.MustParsePrefix("fd01:203:405::/48"),
		ExternalPrefix: netip.MustParsePrefix("2001:db8:1::/48"),
	}
	cfg := ovsconfig.OpenFlowConfig{
		Mappings: map[string]config.NPTv6Translation{"isp": mapping},
		Flows: []ovsconfig.Flow{
			{
				Name:       "forward",
				Connection: "isp",
				Table:      253,
				Actions:    []ovsconfig.Action{{Name: "resubmit", Argument: ",252"}},
			},
		},
	}
	foreign, err := marshalText(instructionExpression, "resubmit(,250)", nil)
	if err != nil {
		t.Fatal(err)
	}

	installed := installedEntries{
		flows: map[flowIdentity]flowContents{
			{table: 251}: {instructions: string(foreign)},
			{table: 249}: {cookie: mwanFlowCookie},
		},
	}
	flows, _, err := compileOpenFlow(t.Context(), &cfg, nil, nil, installed)
	if err != nil {
		t.Fatal(err)
	}

	if len(flows) < 3 {
		t.Fatalf("expected translation helpers, got %d flows", len(flows))
	}

	var usedPrior bool

	for _, flow := range flows[2:] {
		if flow.TableId >= 250 {
			t.Fatalf("helper installed in occupied or referenced table %d", flow.TableId)
		}

		if flow.TableId == 249 {
			usedPrior = true
		}
	}

	if !usedPrior {
		t.Fatal("previously owned table was not reused")
	}

	cfg.Flows[0].Condition = &ovsconfig.Condition{HealthyConnection: "isp"}
	flows, _, err = compileOpenFlow(t.Context(), &cfg, nil, nil, installed)
	if err != nil {
		t.Fatal(err)
	}

	for _, flow := range flows[1:] {
		if flow.TableId >= 250 {
			t.Fatalf("inactive flow did not reserve table %d", flow.TableId)
		}
	}

	cfg.Flows[0].Condition = nil
	cfg.Flows[0].Connection = ""
	flows, _, err = compileOpenFlow(t.Context(), &cfg, nil, nil, installed)
	if err != nil || len(flows) != 2 {
		t.Fatalf("unused mapping installed helpers: %d, %v", len(flows), err)
	}
}

func TestConnectionBucketRequiresExplicitResubmitTable(t *testing.T) {
	mapping := config.NPTv6Translation{
		InternalPrefix: netip.MustParsePrefix("fd00::/48"),
		ExternalSource: config.PrefixDelegated,
	}
	cfg := ovsconfig.OpenFlowConfig{
		Mappings: map[string]config.NPTv6Translation{"isp": mapping},
		Groups: []ovsconfig.Group{
			{
				ID:   1,
				Type: ovsconfig.GroupSelect,
				Buckets: []ovsconfig.Bucket{
					{
						ID:         1,
						Connection: "isp",
						Actions:    []ovsconfig.Action{{Name: "group", Argument: "2"}},
					},
				},
			},
			{
				ID:   2,
				Type: ovsconfig.GroupIndirect,
				Buckets: []ovsconfig.Bucket{
					{ID: 1, Actions: []ovsconfig.Action{{Name: "resubmit", Argument: "1"}}},
				},
			},
		},
	}
	_, _, err := compileOpenFlow(t.Context(), &cfg, nil, nil, installedEntries{})
	if !errors.Is(err, errCurrentTable) {
		t.Fatalf("expected explicit table error, got %v", err)
	}

	cfg.Groups[1].Buckets[0].Actions[0].Argument = "1,123"
	if _, _, err := compileOpenFlow(t.Context(), &cfg, nil, nil, installedEntries{}); err != nil {
		t.Fatal(err)
	}
}
