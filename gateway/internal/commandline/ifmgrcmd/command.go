// Package ifmgrcmd provides the interface manager command.
package ifmgrcmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/gateway/internal/commandline/commandconfig"
	"goodkind.io/mwan/gateway/internal/commandline/commandopts"
	"goodkind.io/mwan/gateway/internal/commandline/commandui"
	"goodkind.io/mwan/gateway/internal/ifmgr"
	"goodkind.io/mwan/gateway/networkjson"
)

var errUnknownIfMgrRole = errors.New("unknown ifmgr role")

// New creates the interface manager command.
//
// The command reads role and logging options, then calls run with the parsed
// values and command output writer.
//
// Example:
//
//	root.AddCommand(ifmgrcmd.New(run))
func New(
	run func(context.Context, string, string, commandopts.Ifmgr, io.Writer) error,
) *cobra.Command {
	command := commandui.Leaf(
		"ifmgr",
		"Run the interface manager for a configured role.",
		"  mwan ifmgr --role wan --config /etc/mwan/config.toml",
		cobra.NoArgs,
	)
	command.Long = strings.Join(
		[]string{
			"Manage local gateway interfaces and WAN forwarding.",
			"Set ifmgr.role in TOML or pass --role.",
			"The WAN role reads WANCONFIG from " + networkjson.DefaultPath +
				" and requires the YANG models in " + networkjson.DefaultSchemaDir + ".",
		},
		" ",
	)
	var path string
	var options commandopts.Ifmgr
	commandconfig.AddFlag(command.Flags(), &path)
	flags := command.Flags()
	flags.StringVar(
		&options.NetworkPath,
		"network",
		networkjson.DefaultPath,
		"WANCONFIG JSON document.",
	)
	flags.StringVar(
		&options.SchemaDir,
		"schema-dir",
		networkjson.DefaultSchemaDir,
		"Directory containing YANG models.",
	)
	roles := ifmgr.KnownRoles()
	slices.Sort(roles)
	flags.StringVar(
		&options.Role,
		"role",
		"",
		"Override ifmgr.role from TOML. Choose "+strings.Join(roles, ", ")+".",
	)
	flags.BoolVar(
		&options.Debug,
		"debug",
		false,
		"Enable DEBUG logs on standard output. The command also accepts ifmgr.debug from TOML.",
	)
	flags.BoolVar(
		&options.DryRun,
		"dry-run",
		false,
		"Record the dry run setting in the startup log. The interface manager still performs network operations.",
	)
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if options.Role != "" && !slices.Contains(roles, options.Role) {
			err := fmt.Errorf(
				"%w %q; choose %s",
				errUnknownIfMgrRole,
				options.Role,
				strings.Join(roles, ", "),
			)
			slog.Warn("ifmgr: role validation failed", "role", options.Role, "err", err)

			return err
		}

		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		return run(current.Context(), current.CommandPath(), path, options, current.OutOrStdout())
	}

	return command
}
