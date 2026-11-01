package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

const fullHelpTemplate = `{{with (or .Long .Short)}}{{. | trimTrailingWhitespaces}}

{{end}}Usage:{{if .Runnable}}
  {{.UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [command]{{end}}{{if .Aliases}}

Aliases:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

Examples:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

Available Commands:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

Flags:
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

Global Flags:
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}{{end}}
`

func installHelpRendering(command *cobra.Command, started *bool, boundary func(string)) {
	command.SetUsageTemplate(fullHelpTemplate)
	command.SetHelpTemplate(`{{.UsageString}}`)
	command.SilenceUsage = false
	validate := command.PreRunE
	command.PreRunE = func(current *cobra.Command, arguments []string) error {
		if option := current.Flags().Lookup("config"); option != nil && option.Changed && option.Value.String() == "" {
			err := fmt.Errorf("--config requires a nonempty configuration path")
			if isDeploymentOperation(current) {
				return commandExitError{code: exitDeployGateUsage, cause: err}
			}
			return err
		}
		if validate != nil {
			return validate(current, arguments)
		}
		return nil
	}
	for _, child := range command.Commands() {
		installHelpRendering(child, started, boundary)
	}
	if command.RunE != nil {
		execute := command.RunE
		command.RunE = func(current *cobra.Command, arguments []string) error {
			*started = true
			current.SilenceUsage = true
			if current.Parent() != nil && (!current.HasSubCommands() || commandName(current.Name()) == commandWatchdog) {
				top := current
				for top.Parent() != nil && top.Parent().Parent() != nil {
					top = top.Parent()
				}
				boundary(top.Name())
			}
			return execute(current, arguments)
		}
	}
}
