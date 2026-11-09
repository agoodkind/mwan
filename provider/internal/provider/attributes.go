package provider

import (
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const (
	roleNameKey         = "name"
	rolePathKey         = "path"
	contentAttributeKey = "content"
)

func roleUnitsAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Computed: true,
		Description: "The list includes role services and system services that read installed files. " +
			"It uses instance names instead of service templates.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				roleNameKey: schema.StringAttribute{
					Computed:    true,
					Description: "The name identifies a systemd unit.",
				},
				"enabled": schema.BoolAttribute{
					Computed:    true,
					Description: "The value specifies the desired service enablement state.",
				},
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
	}
}

func roleYangModulesAttribute() schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		Computed: true,
		Description: "The data source returns YANG modules in installation order for the wan role. " +
			"The data source returns an empty list for other roles.",
		NestedObject: schema.NestedAttributeObject{
			Attributes: map[string]schema.Attribute{
				"file": schema.StringAttribute{
					Computed:    true,
					Description: "The filename includes the YANG module revision.",
				},
				rolePathKey: schema.StringAttribute{
					Computed:    true,
					Description: "The module destination path is absolute.",
				},
				contentAttributeKey: schema.StringAttribute{
					Computed:    true,
					Description: "The value contains the YANG module source.",
				},
				"mode": schema.StringAttribute{
					Computed:    true,
					Description: "The file mode uses four octal digits.",
				},
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
	}
}
