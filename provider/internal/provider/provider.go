// Package provider exposes MWAN deployment data to OpenTofu.
// It does not install files or manage services.
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

type providerData struct {
	buildCommit    string
	releaseBaseURL string
	client         *http.Client
}

// New accepts the stamped build commit, or an empty string for an unstamped build.
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
		Description: "The provider returns MWAN release and role configuration data. " +
			"It does not install files or manage services.",
		Attributes: map[string]schema.Attribute{
			"release_base_url": schema.StringAttribute{
				Optional: true,
				Description: "Asset URLs use <base>/<version>/<asset>. The default base is " +
					release.DefaultBaseURL + ".",
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
