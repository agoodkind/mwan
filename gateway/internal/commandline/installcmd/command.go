// Package installcmd constructs the local gateway installation command.
// The command validates flags before it calls the installation operation.
package installcmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"goodkind.io/mwan/gateway/installspec"
	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/commandline/commandui"
)

var (
	errInvalidInstallOptions = errors.New("invalid install options")
	errPrintSchemaAndApply   = errors.New("--print-schema and --apply are mutually exclusive")
	errApplyNeedsRole        = errors.New("--apply requires --role")
	errRootMustDiffer        = errors.New("--root must differ from the host root directory")
)

// New creates the local gateway installation command.
//
// The command uses roleNames for role validation and rootIsHost to reject an
// install root that matches the host root.
//
// Example:
//
//	root.AddCommand(installcmd.New(run, roleNames, rootIsHost))
func New(
	run func(commandopts.Install, io.Writer, io.Writer) int,
	roleNames []string,
	rootIsHost func(string) bool,
) *cobra.Command {
	command := commandui.Leaf(
		"install",
		"Install embedded units, policies, and YANG models.",
		"  mwan install --role wan --apply\n  mwan install --print-schema /tmp/schema",
		cobra.NoArgs,
	)
	command.Long = helpDescription(roleNames)
	var options commandopts.Install
	flags := command.Flags()
	flags.StringVar(
		&options.Role,
		"role",
		"",
		"Install files for this gateway role: "+strings.Join(roleNames, ", ")+".",
	)
	flags.BoolVar(&options.Apply, "apply", false, "Write files and enable units.")
	flags.StringVar(
		&options.PrintSchema,
		"print-schema",
		"",
		"Write embedded YANG models to this directory and exit.",
	)
	flags.StringVar(
		&options.Root,
		"root",
		"",
		"Install below this directory without enabling host units.",
	)
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if options.PrintSchema != "" && options.Apply {
			return errPrintSchemaAndApply
		}

		if options.Apply && options.Role == "" {
			return errApplyNeedsRole
		}

		if options.Apply {
			if _, known := installspec.For(installspec.Role(options.Role)); !known {
				return installOptionsError{
					message: fmt.Sprintf(
						"unknown role %q; want one of %s",
						options.Role,
						strings.Join(roleNames, ", "),
					),
				}
			}
		}

		if options.Root != "" && rootIsHost(options.Root) {
			return errRootMustDiffer
		}

		if !options.Apply && options.PrintSchema == "" {
			return pflag.ErrHelp
		}

		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandui.Result(run(options, current.OutOrStdout(), current.ErrOrStderr()))
	}

	return command
}

type installOptionsError struct {
	message string
}

func (err installOptionsError) Error() string {
	return err.message
}

func (err installOptionsError) Unwrap() error {
	return errInvalidInstallOptions
}

func helpDescription(roleNames []string) string {
	var output strings.Builder
	fmt.Fprintln(
		&output,
		strings.Join(
			[]string{
				"Use --apply with --role to install embedded units, policies, and YANG models.",
				"Use --print-schema to write only the YANG models.",
				"The command prints help when neither action is present.",
				"--root uses a private sysrepo repository and does not enable host units.",
			},
			" ",
		),
	)
	fmt.Fprintln(
		&output,
		"A second apply run preserves unchanged file timestamps and re-enables units. The command does not restart services.",
	)
	fmt.Fprintln(&output, "\nFiles installed, by role:")

	for _, name := range roleNames {
		spec, _ := installspec.For(installspec.Role(name))
		paths := make([]string, 0, len(spec.Files))

		for _, file := range spec.Files {
			paths = append(paths, file.Dest)
		}

		fmt.Fprintf(&output, "  %-9s writes %s\n", name, strings.Join(paths, " "))
		fmt.Fprintf(&output, "  %-9s enables %s\n", "", strings.Join(spec.Enable, " "))
		stackModules := spec.StackYangModules()
		modules := make([]string, 0, len(stackModules))

		for _, module := range stackModules {
			modules = append(modules, module.Path())
		}

		if len(modules) > 0 {
			fmt.Fprintf(&output, "  %-9s installs %s\n", "", strings.Join(modules, " "))
		}
	}

	return output.String()
}
