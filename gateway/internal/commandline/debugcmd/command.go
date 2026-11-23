// Package debugcmd builds commands for local gateway inspection.
package debugcmd

import (
	"context"
	"io"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/internal/commandline/commandconfig"
	"goodkind.io/mwan/internal/commandline/commandopts"
	"goodkind.io/mwan/internal/commandline/commandui"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
	"goodkind.io/mwan/internal/networkjson"
)

type runView func(commandopts.Debug, *config.Config, io.Writer, io.Writer) int

type viewHelp struct {
	view         commandopts.DebugView
	arguments    string
	description  string
	argumentHelp string
	maximum      int
}

// Checks supplies network validation operations for the debug command.
type Checks struct {
	OwnedAddresses  func(context.Context, io.Writer) int
	Network         func(string, string, io.Writer) int
	Networkd        func(string, string, string, io.Writer) int
	Release         func(context.Context, io.Writer, string, connectionid.ID, interfaceintent.Owner) int
	Firewall        func(string, string, bool, io.Writer, io.Writer) int
	InspectFirewall func(string, string, io.Writer, io.Writer) int
}

// New creates the local gateway debug command group.
//
// The group registers inspection views and network validation commands. The
// supplied functions run those operations when a command executes.
//
// Example:
//
//	root.AddCommand(debugcmd.New(run, checks))
func New(run runView, checks Checks) *cobra.Command {
	var path string
	command := newDebugRoot(&path)
	command.AddCommand(newViewCommands(&path, run)...)
	command.AddCommand(newCheckCommands(checks)...)

	return command
}

func newDebugRoot(path *string) *cobra.Command {
	command := commandui.Leaf(
		"debug",
		"Inspect gateway state and run interface probes.",
		"  mwan debug status --config /etc/mwan/config.toml",
		cobra.NoArgs,
	)
	command.Long = "Inspect local gateway state, validate network intent, or run interface " +
		"probes. Run mwan debug without a subcommand to list available actions. WAN lists and " +
		"route simulations use " + networkjson.DefaultPath + " and the YANG models in " +
		networkjson.DefaultSchemaDir + "."
	commandconfig.AddFlag(command.PersistentFlags(), path)
	command.RunE = func(current *cobra.Command, _ []string) error { return current.Help() }

	return command
}

func newViewCommands(path *string, run runView) []*cobra.Command {
	views := debugViewHelp()

	commands := make([]*cobra.Command, 0, len(views))
	for _, view := range views {
		commands = append(commands, newViewCommand(view, path, run))
	}

	return commands
}

func debugViewHelp() []viewHelp {
	return []viewHelp{
		{
			commandopts.DebugViewConnectivity,
			"",
			"Inspect addresses, probes, NPT, and prefixes for each WAN.",
			"",
			0,
		},
		{
			commandopts.DebugViewCurl4,
			debugOptionalInterfaceArgs,
			"Check HTTP over IPv4 on an interface.",
			debugIfaceHelp,
			1,
		},
		{
			commandopts.DebugViewCurl6,
			debugOptionalInterfaceArgs,
			"Check HTTP over IPv6 on an interface.",
			debugIfaceHelp,
			1,
		},
		{commandopts.DebugViewLB4, "", "Check IPv4 HTTP distribution across providers.", "", 0},
		{
			commandopts.DebugViewLB4Ifaces,
			"",
			"Check IPv4 HTTP on each configured interface.",
			"",
			0,
		},
		{commandopts.DebugViewLB6, "", "Check IPv6 HTTP distribution across providers.", "", 0},
		{
			commandopts.DebugViewLB6Ifaces,
			"",
			"Check IPv6 HTTP on each configured interface.",
			"",
			0,
		},
		{commandopts.DebugViewNPT, "", "Inspect IPv6 prefix translation rules.", "", 0},
		{
			commandopts.DebugViewPing4,
			debugOptionalInterfaceArgs,
			"Probe IPv4 targets on an interface.",
			debugIfaceHelp,
			1,
		},
		{
			commandopts.DebugViewPing6,
			debugOptionalInterfaceArgs,
			"Probe IPv6 targets on an interface.",
			debugIfaceHelp,
			1,
		},
		{commandopts.DebugViewPolicy, "", "Inspect routing rules and provider tables.", "", 0},
		{commandopts.DebugViewPrefixes, "", "Inspect delegated prefixes for each WAN.", "", 0},
		{commandopts.DebugViewRoutes, "", "Inspect IPv6 DHCP routes.", "", 0},
		{commandopts.DebugViewSim4, "", "Simulate IPv4 provider selection.", "", 0},
		{commandopts.DebugViewSim6, "", "Simulate IPv6 provider selection.", "", 0},
		{commandopts.DebugViewStats, "", "Inspect connection and provider counters.", "", 0},
		{commandopts.DebugViewStatus, "", "Inspect link, address, and prefix state.", "", 0},
		{commandopts.DebugViewSystemd, "", "Inspect gateway systemd units.", "", 0},
		{
			commandopts.DebugViewTraceStart,
			debugOptionalTraceIDArgs,
			"Start a trace and print a network snapshot.",
			"Use trace_id to label the trace. The command generates a label from the current " +
				"time when trace_id is empty.",
			1,
		},
		{commandopts.DebugViewTraceStop, "", "Report the current trace collector state.", "", 0},
		{
			commandopts.DebugViewTraceTail,
			debugOptionalTraceIDArgs,
			"Follow daemon JSON events for an optional trace ID.",
			"Use trace_id to filter events. The command follows all events when trace_id is " +
				"empty.",
			1,
		},
	}
}

func newCheckCommands(checks Checks) []*cobra.Command {
	return []*cobra.Command{
		newCheckOwnedCommand(checks.OwnedAddresses),
		newCheckNetworkCommand(checks.Network),
		newCheckNetworkdCommand(checks.Networkd),
		newCheckReleaseCommand(checks.Release),
		newCheckFirewallCommand(checks.Firewall),
		newInspectFirewallCommand(checks.InspectFirewall),
	}
}

func newViewCommand(view viewHelp, path *string, run runView) *cobra.Command {
	child := commandui.Leaf(
		string(view.view)+view.arguments,
		view.description,
		"  mwan debug "+string(view.view)+" --config /etc/mwan/config.toml",
		cobra.MaximumNArgs(view.maximum),
	)
	if view.argumentHelp != "" {
		child.Long += " " + view.argumentHelp
	}

	var outputFormat string
	var verbose bool
	switch view.view {
	case commandopts.DebugViewPing4,
		commandopts.DebugViewPing6,
		commandopts.DebugViewConnectivity,
		commandopts.DebugViewCurl4,
		commandopts.DebugViewCurl6,
		commandopts.DebugViewLB4,
		commandopts.DebugViewLB6,
		commandopts.DebugViewLB4Ifaces,
		commandopts.DebugViewLB6Ifaces,
		commandopts.DebugViewNPT,
		commandopts.DebugViewPrefixes,
		commandopts.DebugViewRoutes,
		commandopts.DebugViewPolicy,
		commandopts.DebugViewSim4,
		commandopts.DebugViewSim6,
		commandopts.DebugViewStats,
		commandopts.DebugViewStatus,
		commandopts.DebugViewSystemd:
		commandui.OutputFormatFlag(child, &outputFormat, "text")
		child.Flags().BoolVar(
			&verbose,
			"verbose",
			false,
			"Print diagnostic logs to standard error.",
		)
	case commandopts.DebugViewTraceStart,
		commandopts.DebugViewTraceStop,
		commandopts.DebugViewTraceTail:
	}

	child.RunE = func(current *cobra.Command, arguments []string) error {
		return commandconfig.WithConfig(
			current.CommandPath(),
			*path,
			func(cfg *config.Config) error {
				options := commandopts.Debug{
					View:         view.view,
					Arguments:    arguments,
					OutputFormat: outputFormat,
					Verbose:      verbose,
				}

				return commandui.Result(
					run(options, cfg, current.OutOrStdout(), current.ErrOrStderr()),
				)
			},
		)
	}

	return child
}

const (
	debugOptionalInterfaceArgs = " [iface]"
	debugOptionalTraceIDArgs   = " [trace_id]"
	debugIfaceHelp             = "Use iface to select a Linux interface. When the command runs without " +
		"iface, it uses the eligible WAN with the lowest firewall mark."
)
