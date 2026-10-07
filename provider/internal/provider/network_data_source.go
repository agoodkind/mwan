package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"goodkind.io/mwan/internal/networkjson"
)

type networkDataSource struct{}

type networkDataSourceModel struct {
	networkMaps

	Content          types.String `tfsdk:"content"`
	CanonicalContent types.String `tfsdk:"canonical_content"`
}

func newNetworkDataSource() datasource.DataSource {
	return &networkDataSource{}
}

func (d *networkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (d *networkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The data source decodes a network document at plan time and returns canonical JSON and configured maps. " +
			"The data source does not validate the document against the YANG schema.",
		Attributes: map[string]schema.Attribute{
			"content": schema.StringAttribute{
				Required:    true,
				Description: "The content attribute contains the network document as RFC 7951 JSON.",
			},
			"canonical_content": schema.StringAttribute{
				Computed: true,
				Description: "The canonical_content attribute contains compact JSON with sorted object members " +
					"and arrays in the source order.",
			},
			"interfaces": schema.MapAttribute{
				Computed:    true,
				ElementType: networkInterfaceType(),
				Description: interfacesDescription,
			},
			"routes": schema.MapAttribute{
				Computed:    true,
				ElementType: networkRouteType(),
				Description: routesDescription,
			},
			"provider_defaults": schema.MapAttribute{
				Computed:    true,
				ElementType: networkProviderDefaultType(),
				Description: providerDefaultsDescription,
			},
		},
	}
}

func (d *networkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var model networkDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	contentPath := path.Root("content")

	canonical, err := networkjson.Canonicalize([]byte(model.Content.ValueString()))
	if err != nil {
		resp.Diagnostics.AddAttributeError(contentPath, "Invalid network document", err.Error())
		return
	}
	config, err := networkjson.Decode(canonical)
	if err != nil {
		resp.Diagnostics.AddAttributeError(contentPath, "Invalid network configuration", err.Error())
		return
	}
	for _, rejection := range config.Rejected {
		resp.Diagnostics.AddAttributeError(
			contentPath,
			"Rejected provider entry",
			fmt.Sprintf("Interface %s: %v", rejection.Interface, rejection.Err),
		)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	maps, diagnostics := newNetworkMaps(ctx, config)
	resp.Diagnostics.Append(diagnostics...)
	if resp.Diagnostics.HasError() {
		return
	}
	model.CanonicalContent = types.StringValue(string(canonical))
	model.networkMaps = maps
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
