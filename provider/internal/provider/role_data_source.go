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
		Description: "Files and units that `mwan install --role <role>` writes. " +
			"The data source reads the same install list as the mwan binary.",
		Attributes: map[string]schema.Attribute{
			"role": schema.StringAttribute{
				Required:    true,
				Description: "Host role: wan, failover, or host.",
				Validators:  []validator.String{stringvalidator.OneOf(roleNames()...)},
			},
			"files": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Files in write order, with absolute host paths.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"path":    schema.StringAttribute{Computed: true, Description: "Absolute host path."},
						"content": schema.StringAttribute{Computed: true, Description: "File content."},
						"mode":    schema.StringAttribute{Computed: true, Description: "Octal file mode, for example 0644."},
					},
				},
			},
			"binary_path": schema.StringAttribute{
				Computed:    true,
				Description: "Install path of the mwan binary, which the units start.",
			},
			"units": schema.ListNestedAttribute{
				Computed:    true,
				Description: "Units the role enables, in enable order. An instanced unit is a concrete instance.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":    schema.StringAttribute{Computed: true, Description: "Unit name."},
						"enabled": schema.BoolAttribute{Computed: true, Description: "Whether the unit is enabled."},
						"active": schema.BoolAttribute{
							Computed: true,
							Description: "Whether the unit is running after the install. False for a oneshot unit " +
								"without RemainAfterExit, which exits after it runs.",
						},
						"files": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "Paths from files that the unit reads: its unit file, or the template of an " +
								"instance, and the drop-ins of both.",
						},
					},
				},
			},
			"sysrepo_data": schema.ListNestedAttribute{
				Computed: true,
				Description: "Configuration imports into sysrepo datastores, in import order. " +
					"Only the wan role has any.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"datastore": schema.StringAttribute{Computed: true, Description: "startup or running."},
						"module":    schema.StringAttribute{Computed: true, Description: "Module the import configures."},
						"content":   schema.StringAttribute{Computed: true, Description: "XML document to import."},
					},
				},
			},
			"yang_modules": schema.ListNestedAttribute{
				Computed: true,
				Description: "YANG modules the role installs into the wanconfig datastore, " +
					"in install order. Only the wan role has any.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"file":    schema.StringAttribute{Computed: true, Description: "Module file name with its revision."},
						"path":    schema.StringAttribute{Computed: true, Description: "Absolute host path of the module file."},
						"content": schema.StringAttribute{Computed: true, Description: "Module source."},
						"mode":    schema.StringAttribute{Computed: true, Description: "Octal file mode."},
						"features": schema.ListAttribute{
							Computed:    true,
							ElementType: types.StringType,
							Description: "Features to enable at install time.",
						},
						"update": schema.BoolAttribute{
							Computed:    true,
							Description: "Whether an install replaces the module installed at another revision.",
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
