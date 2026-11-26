// Package pdcmd constructs the delegated-prefix inspection command.
package pdcmd

import (
	"io"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/gateway/internal/commandline/commandui"
)

// New builds the delegated-prefix inspection command.
//
// The command passes its interface argument and output writers to run.
//
// Example:
//
//	root.AddCommand(pdcmd.New(run))
func New(run func(string, io.Writer, io.Writer) int) *cobra.Command {
	command := commandui.Leaf(
		"pd <iface>",
		"Print the current delegated IPv6 prefix for an interface.",
		"  mwan pd enwan0",
		commandui.RequiredArguments(1),
	)
	command.Long = "Use iface to select a Linux interface. " +
		"The command reads the current delegated IPv6 prefix and prints it in CIDR notation."
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandui.Result(run(arguments[0], current.OutOrStdout(), current.ErrOrStderr()))
	}

	return command
}
