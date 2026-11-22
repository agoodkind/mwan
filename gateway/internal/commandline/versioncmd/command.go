// Package versioncmd constructs the build and library version command.
package versioncmd

import (
	"io"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/internal/commandline/commandui"
)

// New builds the version command.
//
// The command passes its output writer to run, which prints the compiled
// build and linked library versions.
//
// Example:
//
//	root.AddCommand(versioncmd.New(run))
func New(run func(io.Writer) int) *cobra.Command {
	command := commandui.Leaf(
		"version",
		"Print the compiled build and linked library versions.",
		"  mwan version",
		cobra.NoArgs,
	)
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandui.Result(run(current.OutOrStdout()))
	}

	return command
}
