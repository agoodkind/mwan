package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"goodkind.io/mwan/internal/stackspec"
	"goodkind.io/mwan/provider/internal/release"
)

type releaseDataSource struct {
	data *providerData
}

type releaseModel struct {
	Version       types.String                 `tfsdk:"version"`
	ArchiveMember types.String                 `tfsdk:"archive_member"`
	Architectures map[string]architectureModel `tfsdk:"architectures"`
}

type architectureModel struct {
	MwanURL     types.String      `tfsdk:"mwan_url"`
	MwanSHA256  types.String      `tfsdk:"mwan_sha256"`
	StackURL    types.String      `tfsdk:"stack_url"`
	StackSHA256 types.String      `tfsdk:"stack_sha256"`
	StackDebs   map[string]string `tfsdk:"stack_debs"`
}

func newReleaseDataSource() datasource.DataSource {
	return &releaseDataSource{data: nil}
}

func (d *releaseDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_release"
}

func (d *releaseDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The data source returns the archives of one mwan release. The version must equal the " +
			"release tag of this provider build. The data source downloads checksums.txt of the release.",
		Attributes: map[string]schema.Attribute{
			"version": schema.StringAttribute{
				Required:    true,
				Description: "The release tag of the mwan release, for example 202610050808-b7-b5778cd.",
			},
			"archive_member": schema.StringAttribute{
				Computed:    true,
				Description: "The name of the mwan binary inside the mwan archive.",
			},
			"architectures": schema.MapNestedAttribute{
				Computed:    true,
				Description: "The archives of the release, keyed by the architectures amd64 and arm64.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"mwan_url": schema.StringAttribute{
							Computed:    true,
							Description: "The URL of mwan_linux_<arch>.tar.gz.",
						},
						"mwan_sha256": schema.StringAttribute{
							Computed:    true,
							Description: "The SHA-256 of the mwan archive, as lowercase hex.",
						},
						"stack_url": schema.StringAttribute{
							Computed:    true,
							Description: "The URL of wanconfig-stack_linux_<arch>.tar.gz.",
						},
						"stack_sha256": schema.StringAttribute{
							Computed:    true,
							Description: "The SHA-256 of the wanconfig stack archive, as lowercase hex.",
						},
						"stack_debs": schema.MapAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "This map gives the archive member path, debs/<file>.deb, " +
								"of each runtime package of the wanconfig stack archive.",
						},
					},
				},
			},
		},
	}
}

func (d *releaseDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	data, ok := req.ProviderData.(*providerData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	d.data = data
}

func (d *releaseDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var model releaseModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The provider has no configuration.")
		return
	}

	requested := model.Version.ValueString()
	if d.data.buildCommit == "" {
		resp.Diagnostics.AddError(
			"Provider build has no release version",
			"The release pipeline did not stamp a commit into this provider build. "+
				"Use a released provider build to read release "+requested+".",
		)
		return
	}
	requestedCommit, err := release.CommitOf(requested)
	if err != nil {
		resp.Diagnostics.AddError("Invalid release version", err.Error())
		return
	}
	if requestedCommit != d.data.buildCommit {
		resp.Diagnostics.AddError(
			"Release version differs from the provider build",
			fmt.Sprintf(
				"The configuration asks for release %q, which names commit %q, and this provider build is commit %q. "+
					"Use the provider build of the same release.",
				requested, requestedCommit, d.data.buildCommit,
			),
		)
		return
	}

	architectures, err := release.Resolve(ctx, d.data.client, d.data.releaseBaseURL, requested)
	if err != nil {
		resp.Diagnostics.AddError("Cannot resolve the release", err.Error())
		return
	}
	model.ArchiveMember = types.StringValue(release.ArchiveMember)
	model.Architectures = make(map[string]architectureModel, len(architectures))
	for name, archives := range architectures {
		debs := make(map[string]string, len(stackspec.Packages()))
		for _, pkg := range stackspec.Packages() {
			debs[pkg.Name] = pkg.Member(name)
		}
		model.Architectures[name] = architectureModel{
			StackDebs:   debs,
			MwanURL:     types.StringValue(archives.Mwan.URL),
			MwanSHA256:  types.StringValue(archives.Mwan.SHA256),
			StackURL:    types.StringValue(archives.Stack.URL),
			StackSHA256: types.StringValue(archives.Stack.SHA256),
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}
