package commandline

import (
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Execution records the selected command and its result.
//
// It keeps command diagnostics available when the caller chooses a process
// status.
//
// Example:
//
//	result, exit := Execute[commandui.ExitCodeError](...)
type Execution struct {
	command     *cobra.Command
	diagnostics io.Writer
	err         error
	started     bool
	explicit    bool
}

// Execute builds and runs the command tree with the supplied process streams.
//
// It records whether a handler started so ExitCode can choose the right help
// output.
//
// Example:
//
//	result, exit := Execute[commandui.ExitCodeError](...)
func Execute[E error](
	rootCommand func(*bool, func(string)) *cobra.Command,
	boundary func(string),
	arguments []string,
	input io.Reader,
	output io.Writer,
	diagnostics io.Writer,
) (Execution, E) {
	started := false
	root := rootCommand(&started, boundary)
	root.SetIn(input)
	root.SetOut(output)
	root.SetErr(diagnostics)
	if arguments == nil {
		arguments = []string{}
	}

	root.SetArgs(arguments)
	command, err := root.ExecuteC()
	if command == nil {
		command = root
	}

	exit, explicit := errors.AsType[E](err)

	return Execution{
		command:     command,
		diagnostics: diagnostics,
		err:         err,
		started:     started,
		explicit:    explicit,
	}, exit
}

// ExitCode converts a command result into the process status.
//
// It writes diagnostics and usage text to the configured diagnostics stream.
//
// Example:
//
//	status := result.ExitCode(1, err, usageExitCode)
func (result Execution) ExitCode(code int, cause error, usageCode func(*cobra.Command) int) int {
	if result.err == nil {
		return 0
	}

	showUsage := !result.started || !result.command.SilenceUsage
	if result.explicit {
		if cause != nil {
			fmt.Fprintf(result.diagnostics, "%s: %v\n", result.command.CommandPath(), cause)
		}

		if showUsage {
			fmt.Fprint(result.diagnostics, result.command.UsageString())
		}

		return code
	}

	fmt.Fprintf(result.diagnostics, "%s: %v\n", result.command.CommandPath(), result.err)
	if !showUsage {
		return 1
	}

	fmt.Fprint(result.diagnostics, result.command.UsageString())

	return usageCode(result.command)
}
