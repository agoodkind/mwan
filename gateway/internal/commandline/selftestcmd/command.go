// Package selftestcmd builds the WANCONFIG self-test command.
//
// New accepts the operation that executes the self-test and returns a command
// that validates its private repository options before execution.
//
// Example:
//
//	command := selftestcmd.New(runSelftest)
//	root.AddCommand(command)
package selftestcmd

import (
	"errors"
	"io"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/commandline/commandui"
)

const selftestLong = "Check the WANCONFIG datastore and notifications. " +
	"The command uses the active sysrepo repository by default. " +
	"Provide both --repository and --models-dir to use a private repository. " +
	"The repository directory must be empty or must not exist. " +
	"The models directory must contain the required WANCONFIG YANG models."

var errRepositoryAndModelsTogether = errors.New(
	"--repository and --models-dir must be supplied together",
)

// New builds the WANCONFIG self-test command around the supplied operation.
//
// The command validates that private repository options appear as a pair
// before it calls the operation.
//
// Example:
//
//	command := selftestcmd.New(runSelftest)
//	root.AddCommand(command)
func New(run func(commandopts.Selftest, io.Writer) int) *cobra.Command {
	command := commandui.Leaf(
		"wanconfig-selftest",
		"Exercise the WANCONFIG datastore and notifications.",
		"  mwan wanconfig-selftest --repository /tmp/mwan-selftest --models-dir /usr/local/share/wanconfig/yang",
		cobra.NoArgs,
	)
	command.Long = selftestLong
	var options commandopts.Selftest
	command.Flags().StringVar(
		&options.Repository,
		"repository",
		"",
		"Create a private sysrepo repository in this empty or missing directory.",
	)
	command.Flags().StringVar(
		&options.ModelsDir,
		"models-dir",
		"",
		"Install YANG models from this directory in the private repository.",
	)
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if (options.Repository == "") != (options.ModelsDir == "") {
			return errRepositoryAndModelsTogether
		}

		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandui.Result(run(options, current.ErrOrStderr()))
	}

	return command
}
