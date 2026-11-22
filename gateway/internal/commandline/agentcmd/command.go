// Package agentcmd constructs the gateway API and BGP speaker command.
package agentcmd

import (
	"github.com/spf13/cobra"

	"goodkind.io/mwan/internal/agent"
	"goodkind.io/mwan/internal/commandline/commandconfig"
	"goodkind.io/mwan/internal/config"
)

// New builds the gateway API and BGP speaker command.
//
// The command loads gateway configuration before it starts either service.
// Add the returned command to the root command tree.
//
// Example:
//
//	root.AddCommand(agentcmd.New())
func New() *cobra.Command {
	command := &cobra.Command{
		Use:   "agent",
		Short: "Serve the MWAN gRPC API and BGP speaker.",
		Long: "Start the gateway gRPC API and the BGP speaker when the configuration " +
			"enables BGP. The API listens on TCP and also accepts a Unix socket path.",
		Example: "  mwan agent --config /etc/mwan/config.toml",
		Args:    cobra.NoArgs,
	}
	var path string
	flags := command.Flags()
	commandconfig.AddFlag(flags, &path)
	options := bindAgentFlags(flags)
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandconfig.WithConfig(
			current.CommandPath(),
			path,
			func(cfg *config.Config) error {
				return agent.Run(cfg, agentOptions(flags, *options, cfg.Agent))
			},
		)
	}

	return command
}
