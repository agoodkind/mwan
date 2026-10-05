// Package provider implements the mwan OpenTofu provider. It defines data
// sources only and writes nothing to a host.
package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"goodkind.io/mwan/provider/internal/release"
)

const (
	providerTypeName = "mwan"
	downloadTimeout  = 2 * time.Minute
)

type mwanProvider struct {
	buildCommit string
}

type providerModel struct {
	ReleaseBaseURL types.String `tfsdk:"release_base_url"`
}

// providerData is what the provider hands to its data sources.
type providerData struct {
	buildCommit    string
	releaseBaseURL string
	client         *http.Client
}

// New returns the factory of the mwan provider. buildCommit is the short git
// commit stamped into the provider build, or empty for an unstamped build.
func New(buildCommit string) func() provider.Provider {
	return func() provider.Provider {
		return &mwanProvider{buildCommit: buildCommit}
	}
}

func (p *mwanProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = providerTypeName
	resp.Version = p.buildCommit
}

func (p *mwanProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Reads the files, units, and release archives that an mwan release installs. " +
			"The provider writes nothing to a host.",
		Attributes: map[string]schema.Attribute{
			"release_base_url": schema.StringAttribute{
				Optional: true,
				Description: "Download root of the mwan release assets. The default is " +
					release.DefaultBaseURL + ". An asset is at <root>/<version>/<asset>.",
			},
		},
	}
}

func (p *mwanProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.ReleaseBaseURL.IsUnknown() {
		resp.Diagnostics.AddError(
			"Unknown provider configuration",
			"The release_base_url argument must be known when the provider is configured.",
		)
		return
	}
	baseURL := release.DefaultBaseURL
	if !config.ReleaseBaseURL.IsNull() {
		baseURL = config.ReleaseBaseURL.ValueString()
	}
	resp.DataSourceData = &providerData{
		buildCommit:    p.buildCommit,
		releaseBaseURL: baseURL,
		client:         &http.Client{Timeout: downloadTimeout},
	}
}

func (p *mwanProvider) Resources(_ context.Context) []func() resource.Resource {
	return nil
}

func (p *mwanProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newReleaseDataSource,
		newRoleDataSource,
	}
}
