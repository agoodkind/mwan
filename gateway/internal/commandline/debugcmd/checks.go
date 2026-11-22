package debugcmd

import (
	"context"
	"errors"
	"io"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/internal/commandline/commandconfig"
	"goodkind.io/mwan/internal/commandline/commandui"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

const (
	checkNetworkArgumentCount    = 2
	checkNetworkdArgumentCount   = 3
	checkReleaseArgumentCount    = 2
	checkFirewallArgumentCount   = 2
	inspectFirewallArgumentCount = 2
)

var (
	errEmptyConnectionID    = errors.New("connection ID is empty")
	errInvalidPreviousOwner = errors.New("previous owner must be networkd or mwan")
)

func newCheckOwnedCommand(run func(context.Context, io.Writer) int) *cobra.Command {
	command := commandui.Leaf(
		"check-owned-addresses",
		"Check mapped addresses on gateway interfaces.",
		"  mwan debug check-owned-addresses",
		cobra.NoArgs,
	)
	command.Long = "Check the addresses listed in " + networkjson.DefaultPath +
		" against gateway interfaces. The command requires the YANG models in " +
		networkjson.DefaultSchemaDir + "."
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandui.Result(run(current.Context(), current.OutOrStdout()))
	}

	return command
}

func newCheckNetworkCommand(run func(string, string, io.Writer) int) *cobra.Command {
	command := commandui.Leaf(
		"check-network <network_json> <schema_dir>",
		"Validate network intent and provider configuration.",
		"  mwan debug check-network /etc/mwan/network.json /usr/local/share/wanconfig/yang",
		cobra.ExactArgs(checkNetworkArgumentCount),
	)
	command.Long = "Validate the WANCONFIG JSON file at network_json against the YANG models " +
		"in schema_dir."
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandui.Result(run(arguments[0], arguments[1], current.OutOrStdout()))
	}

	return command
}

func newCheckNetworkdCommand(run func(string, string, string, io.Writer) int) *cobra.Command {
	command := commandui.Leaf(
		"check-networkd <network_json> <schema_dir> <unit_dir>",
		"Compare networkd units with configured network intent.",
		"  mwan debug check-networkd /etc/mwan/network.json /usr/local/share/wanconfig/yang /etc/systemd/network",
		cobra.ExactArgs(checkNetworkdArgumentCount),
	)
	command.Long = "Compare the networkd units in unit_dir with the WANCONFIG JSON file at " +
		"network_json. Validate network_json against the YANG models in schema_dir."
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandui.Result(
			run(arguments[0], arguments[1], arguments[2], current.OutOrStdout()),
		)
	}

	return command
}

func newCheckReleaseCommand(
	run func(context.Context, io.Writer, string, connectionid.ID, interfaceintent.Owner) int,
) *cobra.Command {
	command := commandui.Leaf(
		"check-release <connection_id> <previous_owner>",
		"Verify release from networkd or mwan ownership.",
		"  mwan debug check-release provider-1 networkd --config /etc/mwan/config.toml",
		commandui.RequiredArguments(checkReleaseArgumentCount),
	)
	command.Long = "Verify that the previous owner released the WAN connection identified by " +
		"connection_id. Use networkd or mwan for previous_owner."
	var path string
	var connectionID connectionid.ID
	var previous interfaceintent.Owner
	commandconfig.AddFlag(command.Flags(), &path)
	command.PreRunE = func(_ *cobra.Command, arguments []string) error {
		connectionID, previous = connectionid.ID(arguments[0]), interfaceintent.Owner(arguments[1])
		if connectionID == "" {
			return errEmptyConnectionID
		}

		if previous != interfaceintent.OwnerNetworkd && previous != interfaceintent.OwnerMWAN {
			return errInvalidPreviousOwner
		}

		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandui.Result(
			run(current.Context(), current.OutOrStdout(), path, connectionID, previous),
		)
	}

	return command
}

func newCheckFirewallCommand(
	run func(string, string, bool, io.Writer, io.Writer) int,
) *cobra.Command {
	command := commandui.Leaf(
		"check-firewall <network_json> <schema_dir>",
		"Apply and inspect firewall rules in a separate network namespace.",
		"  mwan debug check-firewall /etc/mwan/network.json /usr/local/share/wanconfig/yang",
		cobra.ExactArgs(checkFirewallArgumentCount),
	)
	command.Long = "Apply and inspect firewall rules from network_json in an isolated network " +
		"namespace. Validate the file against the YANG models in schema_dir."
	var isolated bool
	command.Flags().BoolVar(
		&isolated,
		"isolated",
		false,
		"Use the current network namespace. File descriptor 3 must identify a different caller network namespace.",
	)
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandui.Result(
			run(arguments[0], arguments[1], isolated, current.OutOrStdout(), current.ErrOrStderr()),
		)
	}

	return command
}

func newInspectFirewallCommand(run func(string, string, io.Writer, io.Writer) int) *cobra.Command {
	command := commandui.Leaf(
		"inspect-firewall <network_json> <schema_dir>",
		"Inspect current firewall rules against network intent.",
		"  mwan debug inspect-firewall /etc/mwan/network.json /usr/local/share/wanconfig/yang",
		cobra.ExactArgs(inspectFirewallArgumentCount),
	)
	command.Long = "Compare the current firewall with the WANCONFIG JSON file at network_json. " +
		"Validate the file against the YANG models in schema_dir."
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandui.Result(
			run(arguments[0], arguments[1], current.OutOrStdout(), current.ErrOrStderr()),
		)
	}

	return command
}
