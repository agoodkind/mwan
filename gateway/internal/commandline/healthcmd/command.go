// Package healthcmd constructs the gateway health probe command.
package healthcmd

import (
	"context"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/internal/commandline/commandconfig"
	"goodkind.io/mwan/internal/commandline/commandopts"
	"goodkind.io/mwan/internal/commandline/commandui"
)

// New builds the WAN health probe command.
//
// The supplied operation runs the configured checks and writes command
// output.
// The command cancels that operation when it receives SIGINT or SIGTERM.
//
// Example:
//
//	root.AddCommand(healthcmd.New(run))
func New(
	run func(context.Context, string, commandopts.Health, io.Writer, io.Writer) error,
) *cobra.Command {
	command := commandui.Leaf(
		"health",
		"Probe each configured WAN once or repeat with --watch.",
		"  mwan health --config /etc/mwan/config.toml\n  mwan health --watch --output-format json",
		cobra.NoArgs,
	)
	command.Long = "Run health probes for each enabled WAN. " +
		"This command requires the WANCONFIG JSON document and its YANG models. " +
		"The command exits after one sweep unless --watch is set. " +
		"Finite JSON output contains an array. With --watch, JSON output contains one record per line."
	var path string
	var options commandopts.Health
	commandconfig.AddFlag(command.Flags(), &path)
	commandui.NetworkFlags(command, &options.NetworkPath, &options.SchemaDir)
	commandui.OutputFormatFlag(command, &options.OutputFormat, "text")
	command.Flags().BoolVar(
		&options.Watch,
		"watch",
		false,
		"Repeat each WAN sweep after its configured check interval.",
	)
	command.Flags().BoolVar(
		&options.Verbose,
		"verbose",
		false,
		"Print diagnostic logs to standard error.",
	)
	command.RunE = func(current *cobra.Command, _ []string) error {
		ctx, cancel := signal.NotifyContext(current.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()

		return run(ctx, path, options, current.OutOrStdout(), current.ErrOrStderr())
	}

	return command
}
