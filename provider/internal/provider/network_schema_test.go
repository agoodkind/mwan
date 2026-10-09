package provider_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"goodkind.io/mwan/internal/networkjson"
)

const (
	gatewayInstances = "../../../gateway/yang/instances/"

	documentSummary      = "Invalid network document"
	schemaSummary        = "Invalid network schema"
	rejectedEntrySummary = "Rejected provider entry"
)

func TestNetworkAcceptsSchemaValidDocuments(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	for _, name := range []string{"network-min.json", "network-freeform.json", "network-routes.json"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document, err := os.ReadFile(gatewayInstances + name)
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			state := server.mustReadNetwork(t, string(document))
			wantCanonical, err := networkjson.Canonicalize(document)
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			if got := text(t, state, "canonical_content"); got != string(wantCanonical) {
				t.Errorf("canonical_content = %s, want %s", got, wantCanonical)
			}
			if got := objectMap(t, state, "interfaces"); len(got) == 0 {
				t.Error("interfaces is empty")
			}
		})
	}
}

func TestNetworkRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	cases := []struct {
		name        string
		old         string
		replacement string
		summary     string
		detail      string
	}{
		{
			name:        "unknown member",
			old:         networkManagementEntry,
			replacement: `{ "name": "enmgmt0", "type": "iana-if-type:other", "unknown-member": true }`,
			summary:     schemaSummary,
			detail:      "unknown-member",
		},
		{
			name:        "invalid enum",
			old:         `"hash-mode": "source"`,
			replacement: `"hash-mode": "bogus"`,
			summary:     schemaSummary,
			detail:      "bogus",
		},
		{
			name:        "out-of-range value",
			old:         `"ping-count": 3,` + "\n" + `            "success-threshold": 2,`,
			replacement: `"ping-count": 256,` + "\n" + `            "success-threshold": 2,`,
			summary:     schemaSummary,
			detail:      "256",
		},
		{
			name:        "missing mandatory node",
			old:         networkManagementEntry,
			replacement: `{ "name": "enmgmt0" }`,
			summary:     schemaSummary,
			detail:      "type",
		},
		{
			name:        "explicit null leaf",
			old:         `"forced-dscp": 8`,
			replacement: `"forced-dscp": null`,
			summary:     schemaSummary,
			detail:      "uint8 value",
		},
		{
			name:        "duplicate member",
			old:         `{ "name": "enmwanbr0",`,
			replacement: `{ "name": "enmwanbr0", "name": "enmwanbr0",`,
			summary:     documentSummary,
			detail:      `object member "name" appears more than once`,
		},
		{
			name:        "case-variant member",
			old:         `{ "name": "enmwanbr0",`,
			replacement: `{ "name": "enmwanbr0", "Name": "enmwanbr0",`,
			summary:     documentSummary,
			detail:      `"Name"`,
		},
		{
			name:        "lone surrogate",
			old:         `"https://example.test/mb"`,
			replacement: `"https://example.test/\ud800"`,
			summary:     documentSummary,
			detail:      "surrogate",
		},
		{
			name:        "host-bit route destination",
			old:         `"destination": "198.18.1.0/24"`,
			replacement: `"destination": "198.18.1.1/24"`,
			summary:     rejectedEntrySummary,
			detail:      "Interface enwebpass0: interface enwebpass0: ipv4 route destination",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, diagnostics := server.readNetwork(t, readNetworkDocument(t, test.old, test.replacement))
			if len(diagnostics) != 1 {
				t.Fatalf("diagnostics = %q, want one diagnostic", diagnosticText(diagnostics))
			}
			diagnostic := diagnostics[0]
			if diagnostic.Severity != tfprotov6.DiagnosticSeverityError {
				t.Errorf("severity = %v, want error", diagnostic.Severity)
			}
			if diagnostic.Summary != test.summary {
				t.Errorf("summary = %q, want %q", diagnostic.Summary, test.summary)
			}
			if !strings.Contains(diagnostic.Detail, test.detail) {
				t.Errorf("detail = %q, want %q", diagnostic.Detail, test.detail)
			}
		})
	}
}

func TestNetworkDefersUnknownContent(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	unknown := tftypes.NewValue(tftypes.String, tftypes.UnknownValue)
	response, err := server.server.ValidateDataResourceConfig(
		context.Background(),
		&tfprotov6.ValidateDataResourceConfigRequest{
			TypeName: networkDataSource,
			Config:   dynamicValue(t, server.dataSource, map[string]tftypes.Value{"content": unknown}),
		},
	)
	if err != nil {
		t.Fatalf("validate unknown content: %v", err)
	}
	if len(response.Diagnostics) > 0 {
		t.Errorf("unknown content diagnostics = %q, want none", diagnosticText(response.Diagnostics))
	}
}
