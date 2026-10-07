package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"goodkind.io/mwan/internal/networkjson"
)

type networkConfigResource struct{}

func newNetworkConfigResource() resource.Resource {
	return &networkConfigResource{}
}

func (r *networkConfigResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network_config"
}

func (r *networkConfigResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The resource stores planned network maps in OpenTofu state. " +
			"The resource makes no network call and writes no guest file.",
		Attributes: map[string]schema.Attribute{
			"interfaces": schema.MapAttribute{
				Required:    true,
				ElementType: networkInterfaceType(),
				Description: interfacesDescription,
			},
			"routes": schema.MapAttribute{
				Required:    true,
				ElementType: networkRouteType(),
				Description: routesDescription,
			},
			"provider_defaults": schema.MapAttribute{
				Required:    true,
				ElementType: networkProviderDefaultType(),
				Description: providerDefaultsDescription,
			},
		},
	}
}

func (r *networkConfigResource) ValidateConfig(
	ctx context.Context,
	req resource.ValidateConfigRequest,
	resp *resource.ValidateConfigResponse,
) {
	var maps networkMaps
	resp.Diagnostics.Append(req.Config.Get(ctx, &maps)...)
	if resp.Diagnostics.HasError() {
		return
	}
	forEachNetworkEntry(ctx, maps.Interfaces, path.Root("interfaces"), &resp.Diagnostics,
		func(key string, entryPath path.Path, model networkInterfaceModel, diagnostics *diag.Diagnostics) {
			if networkKnown(model.Name) && key != model.Name.ValueString() {
				addKeyMismatch(diagnostics, entryPath, key, model.Name.ValueString())
			}
		})
	forEachNetworkEntry(ctx, maps.Routes, path.Root("routes"), &resp.Diagnostics,
		func(key string, entryPath path.Path, model networkRouteModel, diagnostics *diag.Diagnostics) {
			if identity, known := model.identity(); known && key != identity {
				addKeyMismatch(diagnostics, entryPath, key, identity)
			}
			validateNetworkFamily(diagnostics, entryPath, model.Family)
			validateRouteSource(diagnostics, entryPath, model.Source)
		})
	forEachNetworkEntry(ctx, maps.ProviderDefaults, path.Root("provider_defaults"), &resp.Diagnostics,
		func(key string, entryPath path.Path, model networkProviderDefaultModel, diagnostics *diag.Diagnostics) {
			if identity, known := model.identity(); known && key != identity {
				addKeyMismatch(diagnostics, entryPath, key, identity)
			}
			validateNetworkFamily(diagnostics, entryPath, model.Family)
		})
}

type networkEntryModel interface {
	networkInterfaceModel | networkRouteModel | networkProviderDefaultModel
}

func forEachNetworkEntry[Model networkEntryModel](
	ctx context.Context,
	entries types.Map,
	mapPath path.Path,
	diagnostics *diag.Diagnostics,
	check func(key string, entryPath path.Path, model Model, diagnostics *diag.Diagnostics),
) {
	if entries.IsUnknown() || entries.IsNull() {
		return
	}
	for key, element := range entries.Elements() {
		entryPath := mapPath.AtMapKey(key)
		if element.IsUnknown() {
			continue
		}
		if element.IsNull() {
			diagnostics.AddAttributeError(entryPath, "Null network map entry", "Each map entry must be an object.")
			continue
		}
		object, isObject := element.(types.Object)
		if !isObject {
			diagnostics.AddAttributeError(entryPath, "Invalid network map entry", fmt.Sprintf("got %T", element))
			continue
		}
		var model Model
		objectDiagnostics := object.As(ctx, &model, basetypes.ObjectAsOptions{
			UnhandledNullAsEmpty:    false,
			UnhandledUnknownAsEmpty: false,
		})
		diagnostics.Append(objectDiagnostics...)
		if objectDiagnostics.HasError() {
			continue
		}
		check(key, entryPath, model, diagnostics)
	}
}

func addKeyMismatch(diagnostics *diag.Diagnostics, entryPath path.Path, key string, identity string) {
	diagnostics.AddAttributeError(
		entryPath,
		"Network map key differs from the entry identity",
		fmt.Sprintf("The key %q must equal %q, which the entry's fields derive.", key, identity),
	)
}

func validateNetworkFamily(diagnostics *diag.Diagnostics, entryPath path.Path, family types.String) {
	allowed := []networkjson.Family{networkjson.FamilyIPv4, networkjson.FamilyIPv6}
	validateNetworkValue(diagnostics, entryPath.AtName("family"), family, allowed)
}

func validateRouteSource(diagnostics *diag.Diagnostics, entryPath path.Path, source types.String) {
	allowed := []networkjson.RouteSource{networkjson.RouteSourceRoute, networkjson.RouteSourceGateway}
	validateNetworkValue(diagnostics, entryPath.AtName("source"), source, allowed)
}

func validateNetworkValue[Value ~string](
	diagnostics *diag.Diagnostics,
	valuePath path.Path,
	value types.String,
	allowed []Value,
) {
	if value.IsUnknown() {
		return
	}
	for _, candidate := range allowed {
		if !value.IsNull() && value.ValueString() == string(candidate) {
			return
		}
	}
	diagnostics.AddAttributeError(
		valuePath,
		"Invalid network value",
		fmt.Sprintf("The value %s must be one of %q.", value.String(), allowed),
	)
}

func (r *networkConfigResource) Create(_ context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	resp.State.Raw = req.Plan.Raw
}

// Read returns the framework's prior state because the resource has no remote state to refresh.
func (r *networkConfigResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {
}

func (r *networkConfigResource) Update(_ context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.State.Raw = req.Plan.Raw
}

func (r *networkConfigResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.State.RemoveResource(ctx)
}
