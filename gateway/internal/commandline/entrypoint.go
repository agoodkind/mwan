// Package commandline creates and executes the mwan command tree.
package commandline

import (
	"errors"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/gateway/internal/commandline/agentcmd"
	"goodkind.io/mwan/gateway/internal/commandline/commandui"
	"goodkind.io/mwan/gateway/internal/commandline/debugcmd"
	"goodkind.io/mwan/gateway/internal/commandline/healthcmd"
	"goodkind.io/mwan/gateway/internal/commandline/ifmgrcmd"
	"goodkind.io/mwan/gateway/internal/commandline/installcmd"
	"goodkind.io/mwan/gateway/internal/commandline/pdcmd"
	"goodkind.io/mwan/gateway/internal/commandline/selftestcmd"
	"goodkind.io/mwan/gateway/internal/commandline/statuscmd"
	"goodkind.io/mwan/gateway/internal/commandline/versioncmd"
	"goodkind.io/mwan/gateway/version"
)

type commandName string

const (
	commandInstall       commandName = "install"
	commandVersion       commandName = "version"
	commandSelftest      commandName = "wanconfig-selftest"
	commandCheckOwned    commandName = "check-owned-addresses"
	commandCheckNetwork  commandName = "check-network"
	commandCheckNetworkd commandName = "check-networkd"
	commandCheckRelease  commandName = "check-release"
	commandCheckFirewall commandName = "check-firewall"
	commandInspectFW     commandName = "inspect-firewall"
	commandUsageExitCode             = 2
)

var errProcessArgumentsMissing = errors.New("process arguments do not contain the executable path")

// Run executes the mwan command tree with the supplied arguments and streams.
//
// The input stream supplies command input. The output stream receives command
// results, and the diagnostics stream receives errors and logs. The caller
// owns the logger.
//
// Example:
//
//	code := commandline.Run(arguments, os.Stdin, os.Stdout, os.Stderr, logger)
func Run(
	arguments []string,
	input io.Reader,
	output io.Writer,
	diagnostics io.Writer,
	logger *slog.Logger,
) int {
	boundary := func(subcommand string) {
		logger.Info(
			"mwan boundary",
			"build",
			version.BuildVersionString(),
			"subcommand",
			subcommand,
		)
	}
	result, exit := Execute[commandui.ExitCodeError](
		newRootCommand,
		boundary,
		arguments,
		input,
		output,
		diagnostics,
	)

	return result.ExitCode(int(exit), nil, usageExitCode)
}

// RunProcess executes mwan with the operating system's argument vector.
//
// It removes the executable path before command parsing and closes the logger
// before it returns.
//
// Example:
//
//	code := commandline.RunProcess(os.Args, os.Stdin, os.Stdout, os.Stderr, logger, closer)
func RunProcess(
	processArguments []string,
	input io.Reader,
	output io.Writer,
	diagnostics io.Writer,
	logger *slog.Logger,
	closer io.Closer,
) int {
	var exitCode int
	if len(processArguments) == 0 {
		logger.Error("mwan process arguments are missing", "err", errProcessArgumentsMissing)
		exitCode = 1
	} else {
		exitCode = Run(processArguments[1:], input, output, diagnostics, logger)
	}

	if err := closer.Close(); err != nil {
		logger.Error("mwan logger close failed", "err", err)
	}

	return exitCode
}

func usageExitCode(command *cobra.Command) int {
	for current := command; current != nil; current = current.Parent() {
		switch commandName(current.Name()) {
		case commandCheckOwned,
			commandCheckNetwork,
			commandCheckNetworkd,
			commandCheckRelease,
			commandCheckFirewall,
			commandInspectFW:
			return exitCheckUsage
		case commandInstall, commandVersion, commandSelftest:
		}

		switch commandName(current.Name()) {
		case commandInstall, commandVersion, commandSelftest:
			return commandUsageExitCode
		case commandCheckOwned,
			commandCheckNetwork,
			commandCheckNetworkd,
			commandCheckRelease,
			commandCheckFirewall,
			commandInspectFW:
		}
	}

	return 1
}

func newRootCommand(started *bool, boundary func(string)) *cobra.Command {
	root := &cobra.Command{
		Use:   "mwan",
		Short: "Manage the Multi-WAN gateway.",
		Long: "Use mwan to run gateway services, install gateway files, and inspect network state. " +
			"Run mwan without a command to list available commands.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
	}
	root.Example = "  mwan ifmgr --help\n  mwan debug --help"
	root.RunE = func(command *cobra.Command, _ []string) error { return command.Help() }
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		agentcmd.New(),
		ifmgrcmd.New(runIfMgrPath),
		healthcmd.New(runHealthOptions),
		pdcmd.New(runPDProbe),
		debugcmd.New(
			runDebugOptions,
			debugcmd.Checks{
				OwnedAddresses:  runCheckOwnedAddresses,
				Network:         runCheckNetwork,
				Networkd:        checkNetworkd,
				Release:         runCheckRelease,
				Firewall:        checkFirewall,
				InspectFirewall: inspectFirewall,
			},
		),
		selftestcmd.New(runWanconfigSelftestOptions),
		versioncmd.New(runVersion),
		installcmd.New(runInstallOptions, knownInstallRoles(), rootIsHost),
		statuscmd.New(runGatewayStatusOptions),
	)
	addHelpCommand(root)
	installHelpRendering(root, started, boundary)
	root.SilenceUsage = true

	return root
}
