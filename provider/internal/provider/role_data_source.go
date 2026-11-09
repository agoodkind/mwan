package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/installspec"
	"goodkind.io/mwan/internal/watchdogconfig"
)

const fileModeFormat = "%04o"

type roleDataSource struct{}

type roleModel struct {
	Role             types.String           `tfsdk:"role"`
	GuestType        types.String           `tfsdk:"guest_type"`
	BinaryPath       types.String           `tfsdk:"binary_path"`
	Files            []roleFileModel        `tfsdk:"files"`
	Units            []unitModel            `tfsdk:"units"`
	YangModules      []yangModuleModel      `tfsdk:"yang_modules"`
	SysrepoData      []sysrepoDataModel     `tfsdk:"sysrepo_data"`
	StackYangModules []stackYangModuleModel `tfsdk:"stack_yang_modules"`
}

type stackYangModuleModel struct {
	Name     types.String `tfsdk:"name"`
	Revision types.String `tfsdk:"revision"`
	Path     types.String `tfsdk:"path"`
	Features []string     `tfsdk:"features"`
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

func (d *roleDataSource) Metadata(
	_ context.Context,
	req datasource.MetadataRequest,
	resp *datasource.MetadataResponse,
) {
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

func guestTypeNames() []string {
	guests := watchdogconfig.GuestTypes()

	names := make([]string, 0, len(guests))
	for _, guest := range guests {
		names = append(names, string(guest))
	}

	return names
}

func (d *roleDataSource) Schema(
	_ context.Context,
	_ datasource.SchemaRequest,
	resp *datasource.SchemaResponse,
) {
	resp.Schema = schema.Schema{
		Description: "The data source returns role files, service settings, YANG modules, and sysrepo imports. " +
			"The installer and provider use shared definitions.",
		Attributes: map[string]schema.Attribute{
			"role": schema.StringAttribute{
				Required:    true,
				Description: "Select wan, failover, host, or router.",
				Validators:  []validator.String{stringvalidator.OneOf(roleNames()...)},
			},
			"guest_type": schema.StringAttribute{
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf(guestTypeNames()...)},
				Description: "guest_type accepts qemu or lxc. A null guest_type means qemu. " +
					"For lxc, the data source returns only settings with the net. prefix and " +
					"the comment lines directly above each setting from each sysctl file. " +
					"The data source omits files without such settings. " +
					"guest_type must match the guest-type leaf in the gateway's network.json.",
			},
			"files": schema.ListNestedAttribute{
				Computed:    true,
				Description: "The data source lists files in installation order with absolute destination paths.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"path": schema.StringAttribute{
							Computed:    true,
							Description: "The destination path is absolute.",
						},
						contentAttributeKey: schema.StringAttribute{
							Computed:    true,
							Description: "The value contains the file content.",
						},
						"mode": schema.StringAttribute{
							Computed:    true,
							Description: "The file mode uses four octal digits, such as 0644.",
						},
					},
				},
			},
			"binary_path": schema.StringAttribute{
				Computed:    true,
				Description: "The service commands execute the MWAN binary at this path.",
			},
			"units": roleUnitsAttribute(),
			"sysrepo_data": schema.ListNestedAttribute{
				Computed: true,
				Description: "The data source returns sysrepo imports in order for the wan role. " +
					"The data source returns an empty list for other roles.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"datastore": schema.StringAttribute{
							Computed:    true,
							Description: "The datastore is startup or running.",
						},
						"module": schema.StringAttribute{
							Computed:    true,
							Description: "The import configures this sysrepo module.",
						},
						"content": schema.StringAttribute{
							Computed:    true,
							Description: "The value contains the XML configuration to import.",
						},
					},
				},
			},
			"stack_yang_modules": stackYangModulesAttribute(),
			"yang_modules":       roleYangModulesAttribute(),
		},
	}
}

func (d *roleDataSource) Read(
	ctx context.Context,
	req datasource.ReadRequest,
	resp *datasource.ReadResponse,
) {
	var model roleModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}

	spec, known := installspec.For(installspec.Role(model.Role.ValueString()))
	if !known {
		resp.Diagnostics.AddError(
			"Unknown role",
			fmt.Sprintf("The role %q does not exist.", model.Role.ValueString()),
		)

		return
	}

	guest := config.GuestTypeQEMU
	if !model.GuestType.IsNull() {
		parsedGuest, parseErr := watchdogconfig.ParseGuestType(model.GuestType.ValueString())
		if parseErr != nil {
			resp.Diagnostics.AddError("Invalid guest type", parseErr.Error())
			return
		}

		guest = parsedGuest
	}

	model.Files = make([]roleFileModel, 0, len(spec.Files))
	for _, file := range spec.Files {
		content, install, err := file.Content(guest)
		if err != nil {
			resp.Diagnostics.AddError("Cannot read an embedded file", err.Error())
			return
		}

		if !install {
			continue
		}

		model.Files = append(model.Files, newRoleFile(file.Dest, content))
	}

	if !readRoleYangModules(&model, spec, &resp.Diagnostics) {
		return
	}

	model.StackYangModules = stackYangModuleModels(spec)
	units, err := spec.Units()
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the units", err.Error())
		return
	}

	model.BinaryPath = types.StringValue(installspec.BinaryPath)

	model.Units = make([]unitModel, 0, len(units))
	for _, unit := range units {
		model.Units = append(
			model.Units,
			unitModel{
				Name:    types.StringValue(unit.Name),
				Enabled: types.BoolValue(unit.Enabled),
				Active:  types.BoolValue(unit.Active),
				Files:   unit.Files,
			},
		)
	}

	var policy []byte
	if spec.Schema {
		policy, err = installspec.NACMPolicy()
		if err != nil {
			resp.Diagnostics.AddError("Cannot read the sysrepo data", err.Error())
			return
		}
	}

	imports := spec.SysrepoImports(policy)

	model.SysrepoData = make([]sysrepoDataModel, 0, len(imports))
	for _, entry := range imports {
		model.SysrepoData = append(
			model.SysrepoData,
			sysrepoDataModel{
				Datastore: types.StringValue(string(entry.Datastore)),
				Module:    types.StringValue(entry.Module),
				Content:   types.StringValue(string(entry.Content)),
			},
		)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func stackYangModulesAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Computed: true,
		Description: "The data source returns package YANG modules in installation order for the wan role. " +
			"The stack packages supply these files; mwan does not embed them.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"name": schema.StringAttribute{
					Computed:    true,
					Description: "Sysrepo installs the module with this name.",
				},
				"revision": schema.StringAttribute{
					Computed:    true,
					Description: "Sysrepo installs this schema revision.",
				},
				"path": schema.StringAttribute{
					Computed:    true,
					Description: "The stack package installs the schema file at this absolute path.",
				},
				"features": schema.ListAttribute{
					Computed:    true,
					ElementType: types.StringType,
					Description: "Sysrepo enables these features when installing the module. " +
						"An empty list enables no features.",
				},
			},
		},
	}
}

func stackYangModuleModels(spec installspec.Spec) []stackYangModuleModel {
	stackModules := spec.StackYangModules()

	models := make([]stackYangModuleModel, 0, len(stackModules))
	for _, module := range stackModules {
		models = append(
			models,
			stackYangModuleModel{
				Name:     types.StringValue(module.Name),
				Revision: types.StringValue(module.Revision),
				Path:     types.StringValue(module.Path()),
				Features: module.Features,
			},
		)
	}

	return models
}

func newRoleFile(path string, content []byte) roleFileModel {
	return roleFileModel{
		Path:    types.StringValue(path),
		Content: types.StringValue(string(content)),
		Mode:    types.StringValue(fmt.Sprintf(fileModeFormat, installspec.FileMode.Perm())),
	}
}
