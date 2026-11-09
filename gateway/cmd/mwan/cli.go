package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"goodkind.io/mwan/internal/config"
)

type commandExitError struct {
	code  int
	cause error
}

type commandName string

const (
	commandDeploy     commandName = "deploy"
	commandDeployGate commandName = "deploy-gate"
	commandInstall    commandName = "install"
	commandObserve    commandName = "observe"
	commandVersion    commandName = "version"
	commandSelftest   commandName = "wanconfig-selftest"
	commandWatchdog   commandName = "watchdog"
)

func (exit commandExitError) Error() string {
	if exit.cause != nil {
		return exit.cause.Error()
	}

	return fmt.Sprintf("command exited with status %d", exit.code)
}

func commandResult(code int) error {
	if code == 0 {
		return nil
	}

	return commandExitError{code: code, cause: nil}
}

func run(
	arguments []string,
	input io.Reader,
	output io.Writer,
	diagnostics io.Writer,
	boundary func(string),
) int {
	started := false
	root := newRootCommand(&started, boundary)
	root.SetIn(input)
	root.SetOut(output)
	root.SetErr(diagnostics)
	root.SetArgs(normalizeLegacyFlags(root, arguments))
	command, err := root.ExecuteC()
	if err == nil {
		return 0
	}

	if command == nil {
		command = root
	}

	var result commandExitError
	if errors.As(err, &result) {
		if result.cause != nil {
			fmt.Fprintf(diagnostics, "%s: %v\n", command.CommandPath(), result.cause)
		}

		if command != nil && (!started || !command.SilenceUsage) {
			fmt.Fprint(diagnostics, command.UsageString())
		}

		return result.code
	}

	fmt.Fprintf(diagnostics, "%s: %v\n", command.CommandPath(), err)
	if started && command.SilenceUsage {
		return 1
	}

	fmt.Fprint(diagnostics, command.UsageString())

	return usageExitCode(command)
}

// The Go flag parser accepted a single hyphen before long flag names.
func normalizeLegacyFlags(root *cobra.Command, arguments []string) []string {
	normalized := append([]string(nil), arguments...)
	consumeValue := false
	current := root

	for index, argument := range normalized {
		if consumeValue {
			consumeValue = false
			continue
		}

		if argument == "--" {
			break
		}

		if !strings.HasPrefix(argument, "-") {
			for _, child := range current.Commands() {
				if child.Name() == argument || slices.Contains(child.Aliases, argument) {
					current = child
					break
				}
			}

			continue
		}

		name, _, hasValue := strings.Cut(strings.TrimLeft(argument, "-"), "=")
		option := findLegacyFlag(current, name)
		if option == nil {
			continue
		}

		if !strings.HasPrefix(argument, "--") {
			normalized[index] = "-" + argument
		}

		consumeValue = !hasValue && option.NoOptDefVal == ""
	}

	return normalized
}

func findLegacyFlag(command *cobra.Command, name string) *pflag.Flag {
	if name == "help" {
		command.InitDefaultHelpFlag()
	}

	if option := command.Flags().Lookup(name); option != nil {
		return option
	}

	for current := command; current != nil; current = current.Parent() {
		if option := current.PersistentFlags().Lookup(name); option != nil {
			return option
		}
	}

	return nil
}

func usageExitCode(command *cobra.Command) int {
	if isDeploymentOperation(command) {
		return exitDeployGateFailed
	}

	for current := command; current != nil; current = current.Parent() {
		switch commandName(current.Name()) {
		case commandDeploy, commandDeployGate:
			return exitDeployGateUsage
		case commandInstall, commandObserve, commandVersion, commandSelftest:
			return 2
		case commandWatchdog:
			return 1
		}
	}

	return 1
}

func isDeploymentOperation(command *cobra.Command) bool {
	parent := command.Parent()
	if parent == nil {
		return false
	}

	if commandName(parent.Name()) != commandDeploy &&
		commandName(parent.Name()) != commandDeployGate {
		return false
	}

	switch operationMode(command.Name()) {
	case operationArm,
		operationWatch,
		operationStatus,
		operationLease,
		operationRelease,
		operationVerify,
		operationRecover:
		return true
	}

	return false
}

func newRootCommand(started *bool, boundary func(string)) *cobra.Command {
	root := &cobra.Command{
		Use:           "mwan",
		Short:         "Manage the Multi-WAN gateway.",
		Long:          "Manage gateway services, inspect network state, and verify deployments.",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
	}
	root.Example = "  mwan ifmgr --help\n  mwan help deploy wait-deploy"
	root.RunE = func(command *cobra.Command, _ []string) error { return command.Help() }
	root.CompletionOptions.DisableDefaultCmd = true
	root.AddCommand(
		newAgentCommand(),
		newWatchdogCommand(),
		newIfmgrCommand(),
		newHealthCommand(),
		newNotifyCommand(),
		newPDCommand(),
		newDebugCommand(),
		newTraceCommand(),
		newDeployCommand("deploy"),
		newDeployCommand("deploy-gate"),
		newSelftestCommand(),
		newVersionCommand(),
		newInstallCommand(),
		newObservationCommand(),
		newGatewayStatusCommand(),
		newConfigStateCommand(),
		newReportCommand(),
	)
	installHelpRendering(root, started, boundary)
	root.SilenceUsage = true

	return root
}

func leafCommand(use, description, example string, arguments cobra.PositionalArgs) *cobra.Command {
	return &cobra.Command{
		Use:     use,
		Short:   description,
		Long:    description,
		Example: example,
		Args:    arguments,
	}
}

func requiredArguments(count int) cobra.PositionalArgs {
	return func(command *cobra.Command, arguments []string) error {
		if err := cobra.ExactArgs(count)(command, arguments); err != nil {
			return err
		}

		for index, argument := range arguments {
			if argument == "" {
				return fmt.Errorf("argument %d must not be empty", index+1)
			}
		}

		return nil
	}
}

func configFlag(command *cobra.Command, path *string) {
	addConfigFlag(command.Flags(), path)
}

func addConfigFlag(flags *pflag.FlagSet, path *string) {
	flags.Var(
		&firstConfigPath{path: path},
		"config",
		"Read TOML configuration from this path (default: MWAN_CONFIG or /etc/mwan/config.toml).",
	)
}

type firstConfigPath struct {
	path *string
	set  bool
}

func (value *firstConfigPath) String() string { return *value.path }
func (value *firstConfigPath) Type() string   { return "string" }
func (value *firstConfigPath) Set(path string) error {
	if !value.set {
		*value.path = path
		value.set = true
	}

	return nil
}

func withConfig(command *cobra.Command, path string, execute func(*config.Config) error) error {
	cfg, err := config.LoadPath(path)
	if err != nil {
		slog.Error("load configuration failed", "err", err)
		return fmt.Errorf("load configuration: %w", err)
	}

	if err := execute(cfg); err != nil {
		slog.Error("command failed", "command", command.CommandPath(), "err", err)
		return err
	}

	return nil
}
