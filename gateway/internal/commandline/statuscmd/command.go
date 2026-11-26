// Package statuscmd constructs the gateway status inspection command.
package statuscmd

import (
	"io"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/gateway/internal/commandline/commandconfig"
	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/commandline/commandui"
	"goodkind.io/mwan/gateway/internal/config"
)

// New builds the gateway status inspection command.
//
// The command loads gateway configuration before it calls run to read local
// WAN health and provider state.
//
// Example:
//
//	root.AddCommand(statuscmd.New(run))
func New(run func(commandopts.Status, *config.Config, io.Writer, io.Writer) int) *cobra.Command {
	command := commandui.Leaf(
		"gateway-status",
		"Print gateway health.",
		"  mwan gateway-status --config /etc/mwan/config.toml",
		cobra.NoArgs,
	)
	command.Long = "Read local WAN health state and report each provider verdict and the active tier. " +
		"The command requires the WANCONFIG JSON document and its YANG models to read provider settings."
	var path string
	var options commandopts.Status
	commandconfig.AddFlag(command.Flags(), &path)
	commandui.NetworkFlags(command, &options.NetworkPath, &options.SchemaDir)
	commandui.OutputFormatFlag(command, &options.OutputFormat, "json")
	command.Flags().BoolVar(
		&options.Verbose,
		"verbose",
		false,
		"Print diagnostic logs to standard error.",
	)
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandconfig.WithConfig(
			current.CommandPath(),
			path,
			func(cfg *config.Config) error {
				return commandui.Result(
					run(options, cfg, current.OutOrStdout(), current.ErrOrStderr()),
				)
			},
		)
	}

	return command
}
