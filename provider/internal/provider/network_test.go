package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"math/big"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"goodkind.io/mwan/internal/networkjson"
)

const (
	networkDataSource         = "mwan_network"
	networkConfigResource     = "mwan_network_config"
	networkRoutesDocument     = "testdata/networkplan/network-routes.json"
	networkManagementEntry    = `{ "name": "enmgmt0", "type": "iana-if-type:other" }`
	networkManagementGateways = `{ "name": "enmgmt0", "type": "iana-if-type:other", "enabled": true, ` +
		`"ietf-ip:ipv4": { "goodkind-mwan-steering:gateway": "192.0.2.1", ` +
		`"goodkind-mwan-steering:route-metric": 10 }, ` +
		`"ietf-ip:ipv6": { "goodkind-mwan-steering:gateway": "2001:db8:ff::2" } }`
)

type networkServer struct {
	server     tfprotov6.ProviderServer
	dataSource *tfprotov6.Schema
	resource   *tfprotov6.Schema
}

type networkInterface struct {
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Enabled      *bool    `json:"enabled"`
	Owner        string   `json:"owner"`
	ConnectionID string   `json:"connection_id"`
	Roles        []string `json:"roles"`
	ProviderName *string  `json:"provider_name"`
}

type networkRoute struct {
	Interface   string  `json:"interface"`
	Family      string  `json:"family"`
	Destination string  `json:"destination"`
	Gateway     *string `json:"gateway"`
	TableID     int64   `json:"table_id"`
	Metric      int64   `json:"metric"`
	Source      string  `json:"source"`
}

type networkProviderDefault struct {
	ConnectionID        string `json:"connection_id"`
	Interface           string `json:"interface"`
	Family              string `json:"family"`
	TableID             int64  `json:"table_id"`
	InternalDestination string `json:"internal_destination"`
	InternalInterface   string `json:"internal_interface"`
}

func newNetworkServer(t *testing.T) *networkServer {
	t.Helper()
	configured := newServer(t, buildCommit, "")
	schemaResponse, err := configured.server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("get the provider schema: %v", err)
	}
	failOnDiagnostics(t, "get the provider schema", schemaResponse.Diagnostics)
	resourceSchema, found := schemaResponse.ResourceSchemas[networkConfigResource]
	if !found {
		t.Fatalf("the provider has no resource %s", networkConfigResource)
	}
	return &networkServer{
		server:     configured.server,
		dataSource: configured.schemas[networkDataSource],
		resource:   resourceSchema,
	}
}

func readNetworkDocument(t *testing.T, replacements ...string) string {
	t.Helper()
	data, err := os.ReadFile(networkRoutesDocument)
	if err != nil {
		t.Fatalf("read %s: %v", networkRoutesDocument, err)
	}
	document := string(data)
	for i := 0; i+1 < len(replacements); i += 2 {
		old, replacement := replacements[i], replacements[i+1]
		if count := strings.Count(document, old); count != 1 {
			t.Fatalf("edit target %q occurs %d times, want 1", old, count)
		}
		document = strings.Replace(document, old, replacement, 1)
	}
	return document
}

func (s *networkServer) readNetwork(t *testing.T, content string) (tftypes.Value, []*tfprotov6.Diagnostic) {
	t.Helper()
	ctx := context.Background()
	config := dynamicValue(t, s.dataSource, map[string]tftypes.Value{
		"content": tftypes.NewValue(tftypes.String, content),
	})
	validateResponse, err := s.server.ValidateDataResourceConfig(ctx, &tfprotov6.ValidateDataResourceConfigRequest{
		TypeName: networkDataSource,
		Config:   config,
	})
	if err != nil {
		t.Fatalf("validate %s: %v", networkDataSource, err)
	}
	failOnDiagnostics(t, "validate "+networkDataSource, validateResponse.Diagnostics)
	readResponse, err := s.server.ReadDataSource(ctx, &tfprotov6.ReadDataSourceRequest{
		TypeName: networkDataSource,
		Config:   config,
	})
	if err != nil {
		t.Fatalf("read %s: %v", networkDataSource, err)
	}
	if len(readResponse.Diagnostics) > 0 {
		return tftypes.Value{}, readResponse.Diagnostics
	}
	state, err := readResponse.State.Unmarshal(s.dataSource.ValueType())
	if err != nil {
		t.Fatalf("decode the state of %s: %v", networkDataSource, err)
	}
	return state, nil
}

func (s *networkServer) mustReadNetwork(t *testing.T, content string) tftypes.Value {
	t.Helper()
	state, diagnostics := s.readNetwork(t, content)
	failOnDiagnostics(t, "read "+networkDataSource, diagnostics)
	return state
}

func nullableText(t *testing.T, value tftypes.Value, name string) *string {
	t.Helper()
	found := attribute(t, value, name)
	if found.IsNull() {
		return nil
	}
	var result string
	if err := found.As(&result); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return &result
}

func integer(t *testing.T, value tftypes.Value, name string) int64 {
	t.Helper()
	var number big.Float
	if err := attribute(t, value, name).As(&number); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	result, accuracy := number.Int64()
	if accuracy != big.Exact {
		t.Fatalf("%s = %s, want an integer", name, number.String())
	}
	return result
}

func objectMap(t *testing.T, value tftypes.Value, name string) map[string]tftypes.Value {
	t.Helper()
	var result map[string]tftypes.Value
	if err := attribute(t, value, name).As(&result); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return result
}

func decodeNetworkInterfaces(t *testing.T, state tftypes.Value) map[string]networkInterface {
	t.Helper()
	result := map[string]networkInterface{}
	for key, value := range objectMap(t, state, "interfaces") {
		var enabled *bool
		if found := attribute(t, value, "enabled"); !found.IsNull() {
			var flag bool
			if err := found.As(&flag); err != nil {
				t.Fatalf("decode enabled: %v", err)
			}
			enabled = &flag
		}
		roles := stringList(t, elements(t, value, "roles"))
		slices.Sort(roles)
		result[key] = networkInterface{
			Name:         text(t, value, "name"),
			Type:         text(t, value, "type"),
			Enabled:      enabled,
			Owner:        text(t, value, "owner"),
			ConnectionID: text(t, value, "connection_id"),
			Roles:        roles,
			ProviderName: nullableText(t, value, "provider_name"),
		}
	}
	return result
}

func decodeNetworkRoutes(t *testing.T, state tftypes.Value) map[string]networkRoute {
	t.Helper()
	result := map[string]networkRoute{}
	for key, value := range objectMap(t, state, "routes") {
		result[key] = networkRoute{
			Interface:   text(t, value, "interface"),
			Family:      text(t, value, "family"),
			Destination: text(t, value, "destination"),
			Gateway:     nullableText(t, value, "gateway"),
			TableID:     integer(t, value, "table_id"),
			Metric:      integer(t, value, "metric"),
			Source:      text(t, value, "source"),
		}
	}
	return result
}

func decodeNetworkProviderDefaults(t *testing.T, state tftypes.Value) map[string]networkProviderDefault {
	t.Helper()
	result := map[string]networkProviderDefault{}
	for key, value := range objectMap(t, state, "provider_defaults") {
		result[key] = networkProviderDefault{
			ConnectionID:        text(t, value, "connection_id"),
			Interface:           text(t, value, "interface"),
			Family:              text(t, value, "family"),
			TableID:             integer(t, value, "table_id"),
			InternalDestination: text(t, value, "internal_destination"),
			InternalInterface:   text(t, value, "internal_interface"),
		}
	}
	return result
}

func pointerTo[Value bool | int64 | string](value Value) *Value {
	return &value
}

func providerInterface(name string, connectionID string) networkInterface {
	return networkInterface{
		Name: name, Type: "iana-if-type:other", Enabled: nil, Owner: "networkd",
		ConnectionID: connectionID, Roles: []string{"provider"}, ProviderName: pointerTo(connectionID),
	}
}

func configuredNetworkRoute(
	iface string,
	family string,
	destination string,
	gateway string,
	metric int64,
	source string,
) networkRoute {
	var address *string
	if gateway != "" {
		address = pointerTo(gateway)
	}
	return networkRoute{
		Interface: iface, Family: family, Destination: destination, Gateway: address,
		TableID: 254, Metric: metric, Source: source,
	}
}

func internalProviderDefault(connectionID string, iface string, family string, tableID int64) networkProviderDefault {
	destination := "192.0.2.0/29"
	if family == "ipv6" {
		destination = "2001:db8:b01:fe::2/128"
	}
	return networkProviderDefault{
		ConnectionID: connectionID, Interface: iface, Family: family, TableID: tableID,
		InternalDestination: destination, InternalInterface: "enmwanbr0",
	}
}

func TestNetworkReturnsConfiguredMaps(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	document := readNetworkDocument(t, networkManagementEntry, networkManagementGateways)

	state := server.mustReadNetwork(t, document)

	wantCanonical, err := networkjson.Canonicalize([]byte(document))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if got := text(t, state, "canonical_content"); got != string(wantCanonical) {
		t.Errorf("canonical_content = %s, want %s", got, wantCanonical)
	}
	wantInterfaces := map[string]networkInterface{
		"enwebpass0": providerInterface("enwebpass0", "webpass"),
		"enatt0":     providerInterface("enatt0", "att"),
		"enmbrains0": providerInterface("enmbrains0", "monkeybrains"),
		"enmwanbr0": {
			Name: "enmwanbr0", Type: "iana-if-type:other", Enabled: nil, Owner: "external",
			ConnectionID: "enmwanbr0", Roles: []string{"internal"}, ProviderName: nil,
		},
		"enmgmt0": {
			Name: "enmgmt0", Type: "iana-if-type:other", Enabled: pointerTo(true), Owner: "external",
			ConnectionID: "enmgmt0", Roles: []string{"management"}, ProviderName: nil,
		},
	}
	if got := decodeNetworkInterfaces(t, state); !reflect.DeepEqual(got, wantInterfaces) {
		t.Errorf("interfaces:\ngot  %+v\nwant %+v", got, wantInterfaces)
	}
	wantRoutes := map[string]networkRoute{
		"enwebpass0|ipv4|198.18.0.0/24": configuredNetworkRoute(
			"enwebpass0", "ipv4", "198.18.0.0/24", "203.0.113.1", 0, "route"),
		"enwebpass0|ipv4|198.18.1.0/24": configuredNetworkRoute(
			"enwebpass0", "ipv4", "198.18.1.0/24", "", 0, "route"),
		"enwebpass0|ipv4|198.18.2.0/24": configuredNetworkRoute(
			"enwebpass0", "ipv4", "198.18.2.0/24", "203.0.113.1", 50, "route"),
		"enwebpass0|ipv6|2001:db8:a00::/48": configuredNetworkRoute(
			"enwebpass0", "ipv6", "2001:db8:a00::/48", "2001:db8:ff::1", 0, "route"),
		"enwebpass0|ipv6|2001:db8:a01::/48": configuredNetworkRoute(
			"enwebpass0", "ipv6", "2001:db8:a01::/48", "", 0, "route"),
		"enwebpass0|ipv6|2001:db8:a02::/48": configuredNetworkRoute(
			"enwebpass0", "ipv6", "2001:db8:a02::/48", "2001:db8:ff::1", 2048, "route"),
		"enmgmt0|ipv4|0.0.0.0/0": configuredNetworkRoute(
			"enmgmt0", "ipv4", "0.0.0.0/0", "192.0.2.1", 10, "gateway"),
		"enmgmt0|ipv6|::/0": configuredNetworkRoute(
			"enmgmt0", "ipv6", "::/0", "2001:db8:ff::2", 0, "gateway"),
	}
	if got := decodeNetworkRoutes(t, state); !reflect.DeepEqual(got, wantRoutes) {
		t.Errorf("routes:\ngot  %+v\nwant %+v", got, wantRoutes)
	}
	wantDefaults := map[string]networkProviderDefault{
		"webpass|ipv4|200":      internalProviderDefault("webpass", "enwebpass0", "ipv4", 200),
		"webpass|ipv6|200":      internalProviderDefault("webpass", "enwebpass0", "ipv6", 200),
		"att|ipv4|100":          internalProviderDefault("att", "enatt0", "ipv4", 100),
		"att|ipv6|100":          internalProviderDefault("att", "enatt0", "ipv6", 100),
		"monkeybrains|ipv4|300": internalProviderDefault("monkeybrains", "enmbrains0", "ipv4", 300),
	}
	if got := decodeNetworkProviderDefaults(t, state); !reflect.DeepEqual(got, wantDefaults) {
		t.Errorf("provider_defaults:\ngot  %+v\nwant %+v", got, wantDefaults)
	}
}

func TestNetworkIgnoresWhitespaceAndMemberOrder(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	document := readNetworkDocument(t)
	canonical, err := networkjson.Canonicalize([]byte(document))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, canonical, "", "\t"); err != nil {
		t.Fatalf("indent the canonical document: %v", err)
	}

	original := server.mustReadNetwork(t, document)
	for _, reformatted := range []string{string(canonical), indented.String()} {
		state := server.mustReadNetwork(t, reformatted)
		for _, name := range append([]string{"canonical_content", "guest_type"}, networkMapNames()...) {
			if got, want := attribute(t, state, name), attribute(t, original, name); !got.Equal(want) {
				t.Errorf("%s differs after reformatting:\ngot  %s\nwant %s", name, got, want)
			}
		}
	}
}

func TestNetworkReportsDocumentErrors(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	cases := []struct {
		name         string
		replacements []string
		want         []string
		summary      string
		detail       string
	}{
		{
			name:         "host bits",
			replacements: []string{`"destination": "198.18.1.0/24"`, `"destination": "198.18.1.1/24"`},
			want:         []string{"enwebpass0", "must be a canonical same-family network prefix"},
		},
		{
			name:         "duplicate member",
			replacements: []string{`{ "name": "enmwanbr0",`, `{ "name": "enmwanbr0", "name": "enmwanbr0",`},
			want:         []string{`object member "name" appears more than once`},
		},
		{
			name:         "rejected provider",
			replacements: []string{`"from-prio": 57,`, ``},
			want:         []string{"Rejected provider entry", "enmbrains0", "from-prio is required"},
		},
		{
			name:         "provider table outside uint32",
			replacements: []string{`"table-id": 200`, `"table-id": 4294967296`},
			summary:      "Invalid network schema",
			detail:       `Value "4294967296" is out of type uint32 min/max bounds`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, diagnostics := server.readNetwork(t, readNetworkDocument(t, test.replacements...))
			if len(diagnostics) == 0 {
				t.Fatal("the read returned no diagnostics")
			}
			for _, diagnostic := range diagnostics {
				if diagnostic.Severity != tfprotov6.DiagnosticSeverityError {
					t.Errorf("diagnostic %q has severity %v, want error", diagnostic.Summary, diagnostic.Severity)
				}
			}
			for _, want := range test.want {
				if !strings.Contains(diagnosticText(diagnostics), want) {
					t.Errorf("diagnostics = %q, want %q", diagnosticText(diagnostics), want)
				}
			}
			if test.summary == "" {
				return
			}
			if len(diagnostics) != 1 {
				t.Fatalf("diagnostics = %q, want one diagnostic", diagnosticText(diagnostics))
			}
			if diagnostics[0].Summary != test.summary {
				t.Errorf("summary = %q, want %q", diagnostics[0].Summary, test.summary)
			}
			if !strings.Contains(diagnostics[0].Detail, test.detail) {
				t.Errorf("detail = %q, want %q", diagnostics[0].Detail, test.detail)
			}
		})
	}
}

func TestNetworkValidatesContent(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	cases := []struct {
		name      string
		content   tftypes.Value
		wantError bool
	}{
		{name: "null", content: tftypes.NewValue(tftypes.String, nil), wantError: true},
		{name: "unknown", content: tftypes.NewValue(tftypes.String, tftypes.UnknownValue), wantError: false},
	}
	for _, test := range cases {
		response, err := server.server.ValidateDataResourceConfig(
			context.Background(),
			&tfprotov6.ValidateDataResourceConfigRequest{
				TypeName: networkDataSource,
				Config:   dynamicValue(t, server.dataSource, map[string]tftypes.Value{"content": test.content}),
			},
		)
		if err != nil {
			t.Fatalf("validate %s content: %v", test.name, err)
		}
		if gotError := len(response.Diagnostics) > 0; gotError != test.wantError {
			t.Errorf("%s content diagnostics = %q, want errors %v",
				test.name, diagnosticText(response.Diagnostics), test.wantError)
		}
	}
}

func networkMapNames() []string {
	return []string{
		"interfaces", "routes", "provider_defaults",
		"policy_rules", "firewall_chains", "firewall_rules", "firewall_sets",
	}
}

func (s *networkServer) networkConfig(t *testing.T, state tftypes.Value) tftypes.Value {
	t.Helper()
	attributes := map[string]tftypes.Value{}
	for _, name := range networkMapNames() {
		attributes[name] = attribute(t, state, name)
	}
	return tftypes.NewValue(s.resource.ValueType(), attributes)
}

func replaceAttribute(t *testing.T, value tftypes.Value, name string, replacement tftypes.Value) tftypes.Value {
	t.Helper()
	var attributes map[string]tftypes.Value
	if err := value.As(&attributes); err != nil {
		t.Fatalf("decode an object: %v", err)
	}
	// As returns the input value's map; replacement edits require a separate map.
	edited := maps.Clone(attributes)
	edited[name] = replacement
	return tftypes.NewValue(value.Type(), edited)
}

func replaceRoute(
	t *testing.T,
	config tftypes.Value,
	edit func(routes map[string]tftypes.Value),
) tftypes.Value {
	t.Helper()
	return replaceEntries(t, config, "routes", edit)
}

func replaceEntries(
	t *testing.T,
	config tftypes.Value,
	name string,
	edit func(entries map[string]tftypes.Value),
) tftypes.Value {
	t.Helper()
	entries := maps.Clone(objectMap(t, config, name))
	edit(entries)
	return replaceAttribute(t, config, name, tftypes.NewValue(attribute(t, config, name).Type(), entries))
}

func (s *networkServer) dynamic(t *testing.T, value tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	dynamic, err := tfprotov6.NewDynamicValue(s.resource.ValueType(), value)
	if err != nil {
		t.Fatalf("encode a resource value: %v", err)
	}
	return &dynamic
}

func (s *networkServer) value(t *testing.T, dynamic *tfprotov6.DynamicValue) tftypes.Value {
	t.Helper()
	value, err := dynamic.Unmarshal(s.resource.ValueType())
	if err != nil {
		t.Fatalf("decode a resource value: %v", err)
	}
	return value
}

func (s *networkServer) validateConfig(t *testing.T, config tftypes.Value) []*tfprotov6.Diagnostic {
	t.Helper()
	response, err := s.server.ValidateResourceConfig(context.Background(), &tfprotov6.ValidateResourceConfigRequest{
		TypeName: networkConfigResource,
		Config:   s.dynamic(t, config),
	})
	if err != nil {
		t.Fatalf("validate %s: %v", networkConfigResource, err)
	}
	return response.Diagnostics
}

func TestNetworkConfigValidatesKeysAndValues(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	config := server.networkConfig(t, server.mustReadNetwork(t, readNetworkDocument(t)))
	const routeKey = "enwebpass0|ipv4|198.18.1.0/24"
	unknownRoutes := tftypes.NewValue(attribute(t, config, "routes").Type(), tftypes.UnknownValue)

	cases := []struct {
		name   string
		config tftypes.Value
		want   string
	}{
		{name: "data source maps", config: config, want: ""},
		{name: "unknown routes", config: replaceAttribute(t, config, "routes", unknownRoutes), want: ""},
		{
			name: "mismatched key",
			config: replaceRoute(t, config, func(routes map[string]tftypes.Value) {
				routes["enwebpass0|ipv4|198.18.9.0/24"] = routes[routeKey]
				delete(routes, routeKey)
			}),
			want: `The key "enwebpass0|ipv4|198.18.9.0/24" must equal "` + routeKey + `"`,
		},
		{
			name: "invalid source",
			config: replaceRoute(t, config, func(routes map[string]tftypes.Value) {
				routes[routeKey] = replaceAttribute(t, routes[routeKey], "source", tftypes.NewValue(tftypes.String, "static"))
			}),
			want: `The value "static" must be one of ["route" "gateway"]`,
		},
	}
	for _, test := range cases {
		diagnostics := server.validateConfig(t, test.config)
		if test.want == "" {
			if len(diagnostics) > 0 {
				t.Errorf("%s: diagnostics = %q, want none", test.name, diagnosticText(diagnostics))
			}
			continue
		}
		if !strings.Contains(diagnosticText(diagnostics), test.want) {
			t.Errorf("%s: diagnostics = %q, want %q", test.name, diagnosticText(diagnostics), test.want)
		}
	}
}

func (s *networkServer) planAndApply(t *testing.T, prior tftypes.Value, config tftypes.Value) tftypes.Value {
	t.Helper()
	ctx := context.Background()
	planResponse, err := s.server.PlanResourceChange(ctx, &tfprotov6.PlanResourceChangeRequest{
		TypeName:         networkConfigResource,
		PriorState:       s.dynamic(t, prior),
		ProposedNewState: s.dynamic(t, config),
		Config:           s.dynamic(t, config),
	})
	if err != nil {
		t.Fatalf("plan %s: %v", networkConfigResource, err)
	}
	failOnDiagnostics(t, "plan "+networkConfigResource, planResponse.Diagnostics)
	if len(planResponse.RequiresReplace) > 0 {
		t.Errorf("the plan requires replacement for %v", planResponse.RequiresReplace)
	}
	planned := s.value(t, planResponse.PlannedState)
	if !planned.Equal(config) {
		t.Errorf("planned state differs from the configuration:\ngot  %s\nwant %s", planned, config)
	}

	applyResponse, err := s.server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:       networkConfigResource,
		PriorState:     s.dynamic(t, prior),
		PlannedState:   planResponse.PlannedState,
		Config:         s.dynamic(t, config),
		PlannedPrivate: planResponse.PlannedPrivate,
	})
	if err != nil {
		t.Fatalf("apply %s: %v", networkConfigResource, err)
	}
	failOnDiagnostics(t, "apply "+networkConfigResource, applyResponse.Diagnostics)
	state := s.value(t, applyResponse.NewState)
	if !state.Equal(planned) {
		t.Errorf("applied state differs from the plan:\ngot  %s\nwant %s", state, planned)
	}
	return state
}

func TestNetworkConfigStoresPlannedMaps(t *testing.T) {
	t.Parallel()
	server := newNetworkServer(t)
	ctx := context.Background()
	created := server.networkConfig(t, server.mustReadNetwork(t, readNetworkDocument(t)))
	updated := server.networkConfig(t, server.mustReadNetwork(t, readNetworkDocument(t,
		`{ "destination": "198.18.2.0/24", "gateway": "203.0.113.1", "table-id": 254, "metric": 50 }`,
		`{ "destination": "198.18.2.0/24", "gateway": "203.0.113.9", "table-id": 254, "metric": 60 }`,
		networkManagementEntry, networkManagementGateways,
	)))
	absent := tftypes.NewValue(server.resource.ValueType(), nil)

	state := server.planAndApply(t, absent, created)
	state = server.planAndApply(t, state, updated)

	readResponse, err := server.server.ReadResource(ctx, &tfprotov6.ReadResourceRequest{
		TypeName:     networkConfigResource,
		CurrentState: server.dynamic(t, state),
	})
	if err != nil {
		t.Fatalf("read %s: %v", networkConfigResource, err)
	}
	failOnDiagnostics(t, "read "+networkConfigResource, readResponse.Diagnostics)
	if got := server.value(t, readResponse.NewState); !got.Equal(updated) {
		t.Errorf("read state differs from the prior state:\ngot  %s\nwant %s", got, updated)
	}

	deleteResponse, err := server.server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{
		TypeName:     networkConfigResource,
		PriorState:   server.dynamic(t, state),
		PlannedState: server.dynamic(t, absent),
		Config:       server.dynamic(t, absent),
	})
	if err != nil {
		t.Fatalf("delete %s: %v", networkConfigResource, err)
	}
	failOnDiagnostics(t, "delete "+networkConfigResource, deleteResponse.Diagnostics)
	if got := server.value(t, deleteResponse.NewState); !got.IsNull() {
		t.Errorf("state after delete = %s, want null", got)
	}
}
