package provider

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"goodkind.io/mwan/internal/installspec"
	moduleschema "goodkind.io/mwan/internal/yangpub/schema"
)

const fileModeFormat = "%04o"

type roleDataSource struct{}

type roleModel struct {
	Role        types.String       `tfsdk:"role"`
	BinaryPath  types.String       `tfsdk:"binary_path"`
	Files       []roleFileModel    `tfsdk:"files"`
	Units       []unitModel        `tfsdk:"units"`
	YangModules []yangModuleModel  `tfsdk:"yang_modules"`
	SysrepoData []sysrepoDataModel `tfsdk:"sysrepo_data"`
}

type unitModel struct {
	Name    types.String `tfsdk:"name"`
	Enabled types.Bool   `tfsdk:"enabled"`
	Active  types.Bool   `tfsdk:"active"`
	Files   []string     `tfsdk:"files"`
}

type sysrepoDataModel struct {
	Datastore types.String `tfsdk:"datastore"`
	Module    types.String `tfsdk:"module"`
	Content   types.String `tfsdk:"content"`
}

type roleFileModel struct {
	Path    types.String `tfsdk:"path"`
	Content types.String `tfsdk:"content"`
	Mode    types.String `tfsdk:"mode"`
}

type yangModuleModel struct {
	File     types.String `tfsdk:"file"`
	Path     types.String `tfsdk:"path"`
	Content  types.String `tfsdk:"content"`
	Mode     types.String `tfsdk:"mode"`
	Features []string     `tfsdk:"features"`
	Update   types.Bool   `tfsdk:"update"`
}

func newRoleDataSource() datasource.DataSource {
	return &roleDataSource{}
}

func (d *roleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role"
}

func roleNames() []string {
	roles := installspec.Roles()
	names := make([]string, 0, len(roles))
	for _, role := range roles {
		names = append(names, string(role))
	}
	return names
}

func (d *roleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The data source returns role files, service settings, YANG modules, and sysrepo imports. " +
			"The installer and provider use shared definitions.",
		Attributes: map[string]schema.Attribute{
			"role": schema.StringAttribute{
				Required:    true,
				Description: "Select wan, failover, or host.",
				Validators:  []validator.String{stringvalidator.OneOf(roleNames()...)},
			},
			"files": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The data source lists files in installation order with absolute destination paths.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"path":    schema.StringAttribute{Computed: true, Description: "The destination path is absolute."},
						"content": schema.StringAttribute{Computed: true, Description: "The value contains the file content."},
						"mode":    schema.StringAttribute{Computed: true, Description: "The file mode uses four octal digits, such as 0644."},
					},
				},
			},
			"binary_path": schema.StringAttribute{
				Computed:    true,
				Description: "The service commands execute the MWAN binary at this path.",
			},
			"units": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The list includes role services and system services that read installed files. It uses instance names instead of service templates.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":    schema.StringAttribute{Computed: true, Description: "The name identifies a systemd unit."},
						"enabled": schema.BoolAttribute{Computed: true, Description: "The value specifies the desired service enablement state."},
						"active": schema.BoolAttribute{
							Computed: true,
							Description: "The value specifies the desired active state. It is false for a oneshot unit " +
								"without RemainAfterExit.",
						},
						"files": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "These installation paths configure the service: its unit or instance template " +
								"and their drop-ins.",
						},
					},
				},
			},
			"sysrepo_data": schema.ListNestedAttribute{
				Computed: true,
				Description: "The data source returns sysrepo imports in order for the wan role. " +
					"The data source returns an empty list for other roles.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"datastore": schema.StringAttribute{Computed: true, Description: "The datastore is startup or running."},
						"module":    schema.StringAttribute{Computed: true, Description: "The import configures this sysrepo module."},
						"content":   schema.StringAttribute{Computed: true, Description: "The value contains the XML configuration to import."},
					},
				},
			},
			"yang_modules": schema.ListNestedAttribute{
				Computed: true,
				Description: "The data source returns YANG modules in installation order for the wan role. " +
					"The data source returns an empty list for other roles.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"file":    schema.StringAttribute{Computed: true, Description: "The filename includes the YANG module revision."},
						"path":    schema.StringAttribute{Computed: true, Description: "The module destination path is absolute."},
						"content": schema.StringAttribute{Computed: true, Description: "The value contains the YANG module source."},
						"mode":    schema.StringAttribute{Computed: true, Description: "The file mode uses four octal digits."},
						"features": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "The installer enables these YANG features.",
						},
						"update": schema.BoolAttribute{
							Computed:    true,
							Description: "The value permits replacement of a different installed module revision.",
						},
					},
				},
			},
		},
	}
}

func (d *roleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var model roleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	spec, known := installspec.For(installspec.Role(model.Role.ValueString()))
	if !known {
		resp.Diagnostics.AddError("Unknown role", fmt.Sprintf("The role %q does not exist.", model.Role.ValueString()))
		return
	}

	model.Files = make([]roleFileModel, 0, len(spec.Files))
	for _, file := range spec.Files {
		content, err := installspec.Read(file.Embedded)
		if err != nil {
			resp.Diagnostics.AddError("Cannot read an embedded file", err.Error())
			return
		}
		model.Files = append(model.Files, newRoleFile(file.Dest, content))
	}
	model.YangModules = make([]yangModuleModel, 0, len(moduleschema.Modules()))
	if spec.Schema {
		for _, module := range moduleschema.Modules() {
			content, err := moduleschema.Read(module.File)
			if err != nil {
				resp.Diagnostics.AddError("Cannot read an embedded YANG module", err.Error())
				return
			}
			features := module.Features
			if features == nil {
				features = []string{}
			}
			model.YangModules = append(model.YangModules, yangModuleModel{
				File:     types.StringValue(module.File),
				Path:     types.StringValue(filepath.Join(moduleschema.InstallDir, module.File)),
				Content:  types.StringValue(string(content)),
				Mode:     types.StringValue(fmt.Sprintf(fileModeFormat, moduleschema.FileMode.Perm())),
				Features: features,
				Update:   types.BoolValue(module.Update),
			})
		}
	}
	units, err := spec.Units()
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the units", err.Error())
		return
	}
	model.BinaryPath = types.StringValue(installspec.BinaryPath)
	model.Units = make([]unitModel, 0, len(units))
	for _, unit := range units {
		model.Units = append(model.Units, unitModel{
			Name:    types.StringValue(unit.Name),
			Enabled: types.BoolValue(unit.Enabled),
			Active:  types.BoolValue(unit.Active),
			Files:   unit.Files,
		})
	}
	imports, err := spec.SysrepoImports()
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the sysrepo data", err.Error())
		return
	}
	model.SysrepoData = make([]sysrepoDataModel, 0, len(imports))
	for _, entry := range imports {
		model.SysrepoData = append(model.SysrepoData, sysrepoDataModel{
			Datastore: types.StringValue(string(entry.Datastore)),
			Module:    types.StringValue(entry.Module),
			Content:   types.StringValue(string(entry.Content)),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func newRoleFile(path string, content []byte) roleFileModel {
	return roleFileModel{
		Path:    types.StringValue(path),
		Content: types.StringValue(string(content)),
		Mode:    types.StringValue(fmt.Sprintf(fileModeFormat, installspec.FileMode.Perm())),
	}
}
