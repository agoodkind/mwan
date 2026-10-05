package provider_test

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"goodkind.io/mwan/provider/internal/provider"
)

const (
	releaseDataSource = "mwan_release"
	roleDataSource    = "mwan_role"
)

// protocolServer is a configured provider behind the plugin protocol, the
// boundary OpenTofu uses.
type protocolServer struct {
	server  tfprotov6.ProviderServer
	schemas map[string]*tfprotov6.Schema
}

type roleFile struct {
	Path    string
	Content string
	Mode    string
}

type roleModule struct {
	File     string
	Path     string
	Content  string
	Mode     string
	Features []string
	Update   bool
}

type roleData struct {
	Files       []roleFile
	EnableUnits []string
	Modules     []roleModule
}

type archives struct {
	MwanURL     string
	MwanSHA256  string
	StackURL    string
	StackSHA256 string
}

type releaseData struct {
	ArchiveMember string
	Architectures map[string]archives
}

// newServer starts the provider with the given build commit and release base
// URL. An empty releaseBaseURL keeps the provider default.
func newServer(t *testing.T, buildCommit string, releaseBaseURL string) *protocolServer {
	t.Helper()
	ctx := context.Background()
	server := providerserver.NewProtocol6(provider.New(buildCommit)())()
	schemaResponse, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatalf("get the provider schema: %v", err)
	}
	failOnDiagnostics(t, "get the provider schema", schemaResponse.Diagnostics)

	attributes := map[string]tftypes.Value{}
	if releaseBaseURL != "" {
		attributes["release_base_url"] = tftypes.NewValue(tftypes.String, releaseBaseURL)
	}
	config := dynamicValue(t, schemaResponse.Provider, attributes)
	configureResponse, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{Config: config})
	if err != nil {
		t.Fatalf("configure the provider: %v", err)
	}
	failOnDiagnostics(t, "configure the provider", configureResponse.Diagnostics)
	return &protocolServer{server: server, schemas: schemaResponse.DataSourceSchemas}
}

// readRole reads the mwan_role data source. It returns the diagnostics of a
// failed read.
func (s *protocolServer) readRole(t *testing.T, role string) (roleData, []*tfprotov6.Diagnostic) {
	t.Helper()
	state, diagnostics := s.read(t, roleDataSource, map[string]tftypes.Value{
		"role": tftypes.NewValue(tftypes.String, role),
	})
	if len(diagnostics) > 0 {
		return roleData{Files: nil, EnableUnits: nil, Modules: nil}, diagnostics
	}
	return decodeRole(t, state), nil
}

// readRelease reads the mwan_release data source. It returns the diagnostics of
// a failed read.
func (s *protocolServer) readRelease(t *testing.T, version string) (releaseData, []*tfprotov6.Diagnostic) {
	t.Helper()
	state, diagnostics := s.read(t, releaseDataSource, map[string]tftypes.Value{
		"version": tftypes.NewValue(tftypes.String, version),
	})
	if len(diagnostics) > 0 {
		return releaseData{ArchiveMember: "", Architectures: nil}, diagnostics
	}
	return decodeRelease(t, state), nil
}

func (s *protocolServer) read(
	t *testing.T,
	name string,
	attributes map[string]tftypes.Value,
) (tftypes.Value, []*tfprotov6.Diagnostic) {
	t.Helper()
	ctx := context.Background()
	schema, found := s.schemas[name]
	if !found {
		t.Fatalf("the provider has no data source %s", name)
	}
	config := dynamicValue(t, schema, attributes)

	validateResponse, err := s.server.ValidateDataResourceConfig(ctx, &tfprotov6.ValidateDataResourceConfigRequest{
		TypeName: name,
		Config:   config,
	})
	if err != nil {
		t.Fatalf("validate %s: %v", name, err)
	}
	if len(validateResponse.Diagnostics) > 0 {
		return tftypes.Value{}, validateResponse.Diagnostics
	}

	readResponse, err := s.server.ReadDataSource(ctx, &tfprotov6.ReadDataSourceRequest{
		TypeName: name,
		Config:   config,
	})
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if len(readResponse.Diagnostics) > 0 {
		return tftypes.Value{}, readResponse.Diagnostics
	}
	state, err := readResponse.State.Unmarshal(schema.ValueType())
	if err != nil {
		t.Fatalf("decode the state of %s: %v", name, err)
	}
	return state, nil
}

func dynamicValue(t *testing.T, schema *tfprotov6.Schema, attributes map[string]tftypes.Value) *tfprotov6.DynamicValue {
	t.Helper()
	objectType, ok := schema.ValueType().(tftypes.Object)
	if !ok {
		t.Fatalf("schema value type is %T, want an object", schema.ValueType())
	}
	values := make(map[string]tftypes.Value, len(objectType.AttributeTypes))
	for name, attributeType := range objectType.AttributeTypes {
		value, provided := attributes[name]
		if provided {
			values[name] = value
		} else {
			values[name] = tftypes.NewValue(attributeType, nil)
		}
	}
	dynamic, err := tfprotov6.NewDynamicValue(objectType, tftypes.NewValue(objectType, values))
	if err != nil {
		t.Fatalf("encode a configuration: %v", err)
	}
	return &dynamic
}

func failOnDiagnostics(t *testing.T, operation string, diagnostics []*tfprotov6.Diagnostic) {
	t.Helper()
	for _, diagnostic := range diagnostics {
		t.Errorf("%s: %s: %s", operation, diagnostic.Summary, diagnostic.Detail)
	}
	if len(diagnostics) > 0 {
		t.FailNow()
	}
}

func attribute(t *testing.T, value tftypes.Value, name string) tftypes.Value {
	t.Helper()
	var attributes map[string]tftypes.Value
	if err := value.As(&attributes); err != nil {
		t.Fatalf("decode an object: %v", err)
	}
	found, ok := attributes[name]
	if !ok {
		t.Fatalf("the object has no attribute %s", name)
	}
	return found
}

func text(t *testing.T, value tftypes.Value, name string) string {
	t.Helper()
	var result string
	if err := attribute(t, value, name).As(&result); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return result
}

func elements(t *testing.T, value tftypes.Value, name string) []tftypes.Value {
	t.Helper()
	var result []tftypes.Value
	if err := attribute(t, value, name).As(&result); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return result
}

func stringList(t *testing.T, values []tftypes.Value) []string {
	t.Helper()
	result := make([]string, 0, len(values))
	for _, value := range values {
		var item string
		if err := value.As(&item); err != nil {
			t.Fatalf("decode a string: %v", err)
		}
		result = append(result, item)
	}
	return result
}

func decodeRole(t *testing.T, state tftypes.Value) roleData {
	t.Helper()
	role := roleData{Files: nil, EnableUnits: nil, Modules: nil}
	for _, file := range elements(t, state, "files") {
		role.Files = append(role.Files, roleFile{
			Path:    text(t, file, "path"),
			Content: text(t, file, "content"),
			Mode:    text(t, file, "mode"),
		})
	}
	role.EnableUnits = stringList(t, elements(t, state, "enable_units"))
	for _, module := range elements(t, state, "yang_modules") {
		var update bool
		if err := attribute(t, module, "update").As(&update); err != nil {
			t.Fatalf("decode update: %v", err)
		}
		role.Modules = append(role.Modules, roleModule{
			File:     text(t, module, "file"),
			Path:     text(t, module, "path"),
			Content:  text(t, module, "content"),
			Mode:     text(t, module, "mode"),
			Features: stringList(t, elements(t, module, "features")),
			Update:   update,
		})
	}
	return role
}

func decodeRelease(t *testing.T, state tftypes.Value) releaseData {
	t.Helper()
	release := releaseData{
		ArchiveMember: text(t, state, "archive_member"),
		Architectures: map[string]archives{},
	}
	var architectures map[string]tftypes.Value
	if err := attribute(t, state, "architectures").As(&architectures); err != nil {
		t.Fatalf("decode architectures: %v", err)
	}
	for name, pair := range architectures {
		release.Architectures[name] = archives{
			MwanURL:     text(t, pair, "mwan_url"),
			MwanSHA256:  text(t, pair, "mwan_sha256"),
			StackURL:    text(t, pair, "stack_url"),
			StackSHA256: text(t, pair, "stack_sha256"),
		}
	}
	return release
}
