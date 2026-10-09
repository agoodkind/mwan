package provider

import (
	"context"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/networkload"
	"goodkind.io/mwan/internal/yangschema"
)

const (
	invalidDocumentSummary      = "Invalid network document"
	invalidSchemaSummary        = "Invalid network schema"
	invalidConfigurationSummary = "Invalid network configuration"
	schemaUnavailableSummary    = "Network schema unavailable"
)

type networkDataSource struct{}

type networkDataSourceModel struct {
	networkMaps

	Content          types.String `tfsdk:"content"`
	CanonicalContent types.String `tfsdk:"canonical_content"`
	GuestType        types.String `tfsdk:"guest_type"`
}

func newNetworkDataSource() datasource.DataSource {
	return &networkDataSource{}
}

func (d *networkDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network"
}

func (d *networkDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The data source validates known network content against the YANG schema during planning. " +
			"The data source returns canonical JSON, the decoded guest type, and configured maps.",
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
			"guest_type": schema.StringAttribute{
				Computed:    true,
				Description: guestTypeDescription,
			},
			"policy_rules": schema.MapAttribute{
				Computed:    true,
				ElementType: networkPolicyRuleType(),
				Description: policyRulesDescription,
			},
			"firewall_chains": schema.MapAttribute{
				Computed:    true,
				ElementType: networkFirewallChainType(),
				Description: firewallChainsDescription,
			},
			"firewall_rules": schema.MapAttribute{
				Computed:    true,
				ElementType: networkFirewallRuleType(),
				Description: firewallRulesDescription,
			},
			"firewall_sets": schema.MapAttribute{
				Computed:    true,
				ElementType: networkFirewallSetType(),
				Description: firewallSetsDescription,
			},
		},
	}
}

// Schema and JSON failures use the underlying error because their summaries identify
// the failure stage.
func networkLoadFailure(err error) (string, string) {
	var rejected *networkload.SchemaError
	if errors.As(err, &rejected) {
		return invalidSchemaSummary, rejected.Unwrap().Error()
	}
	var malformed *networkload.JSONError
	if errors.As(err, &malformed) {
		return invalidDocumentSummary, malformed.Unwrap().Error()
	}
	return invalidConfigurationSummary, err.Error()
}

func (d *networkDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var model networkDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	contentPath := path.Root("content")
	content := []byte(model.Content.ValueString())

	canonical, err := networkjson.Canonicalize(content)
	if err != nil {
		resp.Diagnostics.AddAttributeError(contentPath, invalidDocumentSummary, err.Error())
		return
	}
	networkSchema, err := yangschema.LoadEmbedded()
	if err != nil {
		resp.Diagnostics.AddError(schemaUnavailableSummary, err.Error())
		return
	}
	defer networkSchema.Close()
	config, err := networkload.ValidateAndDecode(content, networkSchema)
	if err != nil {
		summary, detail := networkLoadFailure(err)
		resp.Diagnostics.AddAttributeError(contentPath, summary, detail)
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
	model.GuestType = types.StringValue(string(config.GuestType))
	model.networkMaps = maps
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
