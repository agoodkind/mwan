package provider

import (
	"fmt"
	"path/filepath"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"goodkind.io/mwan/internal/installspec"
	moduleschema "goodkind.io/mwan/internal/yangpub/schema"
)

func readRoleYangModules(
	model *roleModel,
	spec installspec.Spec,
	diagnostics *diag.Diagnostics,
) bool {
	model.YangModules = make([]yangModuleModel, 0, len(moduleschema.Modules()))
	if spec.Schema || spec.ValidationSchema {
		for _, module := range moduleschema.Modules() {
			content, err := moduleschema.Read(module.File)
			if err != nil {
				diagnostics.AddError("Cannot read an embedded YANG module", err.Error())

				return false
			}

			path := filepath.Join(moduleschema.InstallDir, module.File)
			if !spec.Schema {
				model.Files = append(
					model.Files,
					roleFileModel{
						Path:    types.StringValue(path),
						Content: types.StringValue(string(content)),
						Mode: types.StringValue(
							fmt.Sprintf(fileModeFormat, moduleschema.FileMode.Perm()),
						),
					},
				)

				continue
			}

			features := module.Features
			if features == nil {
				features = []string{}
			}

			model.YangModules = append(
				model.YangModules,
				yangModuleModel{
					File:    types.StringValue(module.File),
					Path:    types.StringValue(path),
					Content: types.StringValue(string(content)),
					Mode: types.StringValue(
						fmt.Sprintf(fileModeFormat, moduleschema.FileMode.Perm()),
					),
					Features: features,
					Update:   types.BoolValue(module.Update),
				},
			)
		}
	}

	return true
}
