package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/installspec"
	"goodkind.io/mwan/internal/observation"
	"goodkind.io/mwan/internal/watchdogconfig"
)

func newInstallCommand() *cobra.Command {
	command := leafCommand("install", "Install embedded units, policies, and YANG models.", "  mwan install --role wan --apply\n  mwan install --print-schema /tmp/schema", cobra.NoArgs)
	command.Long = installHelpDescription()
	var options installFlags
	flags := command.Flags()
	flags.StringVar(&options.role, "role", "", "Install files for this host role: "+strings.Join(knownInstallRoles(), ", ")+".")
	flags.BoolVar(&options.apply, "apply", false, "Write files and enable units.")
	flags.StringVar(&options.printSchema, "print-schema", "", "Write embedded YANG models to this directory and exit.")
	flags.StringVar(&options.root, "root", "", "Install below this directory without enabling host units.")
	flags.StringVar(&options.guestType, "guest-type", string(config.GuestTypeQEMU), "Select qemu or lxc guest settings.")
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if _, err := watchdogconfig.ParseGuestType(options.guestType); err != nil {
			slog.Error("guest type rejected", "err", err)
			return fmt.Errorf("guest type: %w", err)
		}
		if options.printSchema != "" && options.apply {
			return fmt.Errorf("--print-schema and --apply are mutually exclusive")
		}
		if options.apply && options.role == "" {
			return fmt.Errorf("--apply requires --role")
		}
		if options.apply {
			if _, known := installspec.For(installspec.Role(options.role)); !known {
				return fmt.Errorf("unknown role %q; want one of %s", options.role, strings.Join(knownInstallRoles(), ", "))
			}
		}
		if options.root != "" && rootIsHost(options.root) {
			return fmt.Errorf("--root must differ from the host root directory")
		}
		if !options.apply && options.printSchema == "" {
			return pflag.ErrHelp
		}
		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandResult(runInstallOptions(options, current.OutOrStdout(), current.ErrOrStderr()))
	}
	return command
}

func installHelpDescription() string {
	var output strings.Builder
	fmt.Fprintln(&output, "Install embedded units, policies, and YANG models. Without --apply, the command prints help. --root installs below another directory and uses a private sysrepo repository without enabling host units.")
	fmt.Fprintln(&output, "A second apply run preserves unchanged file timestamps and re-enables units. The command does not restart services.")
	fmt.Fprintln(&output, "\nFiles installed, by role:")
	for _, name := range knownInstallRoles() {
		spec, _ := installspec.For(installspec.Role(name))
		var paths []string
		for _, file := range spec.Files {
			paths = append(paths, file.Dest)
		}
		fmt.Fprintf(&output, "  %-9s writes %s\n", name, strings.Join(paths, " "))
		fmt.Fprintf(&output, "  %-9s enables %s\n", "", strings.Join(spec.Enable, " "))
		var modules []string
		for _, module := range spec.StackYangModules() {
			modules = append(modules, module.Path())
		}
		if len(modules) > 0 {
			fmt.Fprintf(&output, "  %-9s installs %s\n", "", strings.Join(modules, " "))
		}
	}
	return output.String()
}

func newObservationCommand() *cobra.Command {
	command := leafCommand("observe", "Run a typed observation and print JSON results.", "  mwan observe < /tmp/check.json\n  mwan observe --continuous --settings /etc/mwan/checks.json --config /etc/mwan/config.toml", cobra.NoArgs)
	command.Long = "Run a typed observation and print JSON results. Read exactly one CheckSpec JSON object from --check or standard input. --paths supplies a JSON array of current path identities.\n\nWith --continuous, --settings supplies recurring observation settings and --config selects notification configuration. Each completed check prints a result record. Cancellation prints a terminal summary. Continuous mode rejects --check, --paths, and --runtime-settings."
	var check, paths, runtimePath, settingsPath, configPath string
	var continuous bool
	var prepared singleObservationInput
	flags := command.Flags()
	flags.StringVar(&check, "check", "", "Read CheckSpec JSON from this value; an empty value reads standard input.")
	flags.StringVar(&paths, "paths", "[]", "Set current typed path identities as a JSON array.")
	flags.StringVar(&runtimePath, "runtime-settings", "", "Read RuntimeConfig JSON from this file.")
	flags.BoolVar(&continuous, "continuous", false, "Run scheduled checks until cancellation, then print terminal counts.")
	flags.StringVar(&settingsPath, "settings", "", "Read recurring observation settings as JSON.")
	configFlag(command, &configPath)
	command.PreRunE = func(current *cobra.Command, _ []string) error {
		if continuous && (check != "" || runtimePath != "" || flags.Changed("paths")) {
			return fmt.Errorf("continuous observations use --settings")
		}
		if !continuous && (settingsPath != "" || configPath != "") {
			return fmt.Errorf("--settings and --config require --continuous")
		}
		if !continuous {
			decoded, err := prepareSingleObservation(check, paths, current.InOrStdin())
			if err == nil {
				err = observation.Validate(decoded.check)
			}
			if err != nil {
				fmt.Fprintln(current.ErrOrStderr(), err)
				return commandResult(2)
			}
			prepared = decoded
		}
		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		if continuous {
			return commandResult(runContinuousObservation(settingsPath, configPath, current.OutOrStdout(), current.ErrOrStderr()))
		}
		return commandResult(runSingleObservation(prepared, runtimePath, flags.Changed("paths"), current.OutOrStdout(), current.ErrOrStderr()))
	}
	return command
}

func newDebugCommand() *cobra.Command {
	command := leafCommand("debug", "Inspect gateway state and run interface probes.", "  mwan debug status --config /etc/mwan/config.toml", cobra.NoArgs)
	var path string
	addConfigFlag(command.PersistentFlags(), &path)
	command.RunE = missingAction
	views := []struct {
		view                   debugView
		arguments, description string
		maximum                int
	}{
		{debugViewConnectivity, "", "Inspect addresses, probes, NPT, and prefixes for each WAN.", 0},
		{debugViewCurl4, " [iface]", "Check HTTP over IPv4 on an interface.", 1},
		{debugViewCurl6, " [iface]", "Check HTTP over IPv6 on an interface.", 1},
		{debugViewLB4, "", "Check IPv4 HTTP distribution across providers.", 0},
		{debugViewLB4Ifaces, "", "Check IPv4 HTTP on each configured interface.", 0},
		{debugViewLB6, "", "Check IPv6 HTTP distribution across providers.", 0},
		{debugViewLB6Ifaces, "", "Check IPv6 HTTP on each configured interface.", 0},
		{debugViewNPT, "", "Inspect IPv6 prefix translation rules.", 0},
		{debugViewPing4, " [iface]", "Probe IPv4 targets on an interface.", 1},
		{debugViewPing6, " [iface]", "Probe IPv6 targets on an interface.", 1},
		{debugViewPolicy, "", "Inspect routing rules and provider tables.", 0},
		{debugViewPrefixes, "", "Inspect delegated prefixes for each WAN.", 0},
		{debugViewRoutes, "", "Inspect IPv6 DHCP routes.", 0},
		{debugViewSim4, "", "Simulate IPv4 provider selection.", 0},
		{debugViewSim6, "", "Simulate IPv6 provider selection.", 0},
		{debugViewStats, "", "Inspect connection and provider counters.", 0},
		{debugViewStatus, "", "Inspect link, address, and prefix state.", 0},
		{debugViewSystemd, "", "Inspect gateway systemd units.", 0},
		{debugViewTraceStart, " [trace_id]", "Start a trace and print a network snapshot.", 1},
		{debugViewTraceStop, "", "Report the current trace collector state.", 0},
		{debugViewTraceTail, " [trace_id]", "Follow daemon JSON events for an optional trace ID.", 1},
	}
	for _, view := range views {
		child := leafCommand(string(view.view)+view.arguments, view.description, "  mwan debug "+string(view.view)+" --config /etc/mwan/config.toml", cobra.MaximumNArgs(view.maximum))
		child.RunE = func(current *cobra.Command, arguments []string) error {
			return withConfig(current, path, func(cfg *config.Config) error {
				options := debugOptions{view: view.view, arguments: arguments}
				return commandResult(runDebugOptions(options, cfg, current.OutOrStdout(), current.ErrOrStderr()))
			})
		}
		command.AddCommand(child)
	}
	return command
}
