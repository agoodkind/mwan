package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/internal/agent"
	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/healthcheck"
	"goodkind.io/mwan/internal/ifmgr"
	"goodkind.io/mwan/internal/networkjson"
	"goodkind.io/mwan/internal/redteam"
	"goodkind.io/mwan/internal/watchdog"
	"goodkind.io/mwan/internal/watchdogconfig"
)

func newAgentCommand() *cobra.Command {
	command := leafCommand("agent", "Serve the MWAN gRPC API and BGP speaker.", "  mwan agent --config /etc/mwan/config.toml", cobra.NoArgs)
	var path string
	var options config.AgentSection
	defaults := config.AgentDefaults()
	configFlag(command, &path)
	flags := command.Flags()
	flags.Uint32Var(&options.VsockPort, "vsock-port", defaults.VsockPort, "Listen on this virtio-vsock port; zero disables vsock (TOML: agent.vsock_port).")
	flags.StringVar(&options.TCPAddr, "tcp-addr", defaults.TCPAddr, "Listen on this TCP address (TOML: agent.tcp_addr).")
	flags.StringVar(&options.DeployFile, "deploy-file", defaults.DeployFile, "Read the last deploy timestamp from this file (TOML: agent.deploy_file).")
	flags.BoolVar(&options.DeployExpected, "deploy-expected", defaults.DeployExpected, "Report a missing deploy timestamp (TOML: agent.deploy_expected).")
	flags.StringVar(&options.LogFile, "log-file", defaults.LogFile, "Write JSON logs to this file (TOML: agent.log_file).")
	flags.BoolVar(&options.Debug, "debug", defaults.Debug, "Enable gRPC reflection (TOML: agent.debug).")
	command.RunE = func(current *cobra.Command, _ []string) error {
		return withConfig(current, path, func(cfg *config.Config) error {
			if !flags.Changed("vsock-port") {
				options.VsockPort = cfg.Agent.VsockPort
			}
			if !flags.Changed("tcp-addr") {
				options.TCPAddr = cfg.Agent.TCPAddr
			}
			if !flags.Changed("deploy-file") {
				options.DeployFile = cfg.Agent.DeployFile
			}
			if !flags.Changed("deploy-expected") {
				options.DeployExpected = cfg.Agent.DeployExpected
			}
			if !flags.Changed("log-file") {
				options.LogFile = cfg.Agent.LogFile
			}
			if !flags.Changed("debug") {
				options.Debug = cfg.Agent.Debug
			}
			return agent.Run(cfg, options)
		})
	}
	return command
}

func newWatchdogCommand() *cobra.Command {
	command := leafCommand("watchdog", "Monitor gateway health and recover failed deployments.", "  mwan watchdog --config /etc/mwan/config.toml\n  mwan watchdog failover --config /etc/mwan/config.toml", cobra.NoArgs)
	var path string
	var options watchdog.Options
	addConfigFlag(command.PersistentFlags(), &path)
	flags := command.Flags()
	flags.BoolVar(&options.DryRun, "dry-run", false, "Log destructive operations and emails without executing them.")
	flags.StringVar(&options.RedTeam, "red-team", "", "Run this fault scenario; imply dry-run unless --red-team-live is set.")
	flags.BoolVar(&options.RedTeamLive, "red-team-live", false, "Execute real VM operations during a fault scenario.")
	flags.IntVar(&options.RedTeamIterations, "red-team-iterations", 10, "Run this many monitoring iterations during a fault scenario.")
	flags.BoolVar(&options.ListScenarios, "list-scenarios", false, "List the available fault scenarios and exit.")
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if options.RedTeam != "" {
			if _, known := redteam.Presets[options.RedTeam]; !known {
				return fmt.Errorf("unknown scenario %q; use --list-scenarios", options.RedTeam)
			}
		}
		return nil
	}
	command.RunE = func(_ *cobra.Command, _ []string) error {
		cfg, err := watchdogconfig.LoadPath(path)
		if err != nil {
			slog.Error("load watchdog configuration failed", "err", err)
			return fmt.Errorf("load watchdog configuration: %w", err)
		}
		if err := watchdog.Run(cfg, options); err != nil {
			slog.Error("watchdog failed", "err", err)
			return fmt.Errorf("watchdog: %w", err)
		}
		return nil
	}
	failover := leafCommand("failover", "Withdraw primary routes and announce through the failover agent.", "  mwan watchdog failover --config /etc/mwan/config.toml", cobra.NoArgs)
	failover.RunE = func(_ *cobra.Command, _ []string) error {
		cfg, err := watchdogconfig.LoadPath(path)
		if err != nil {
			slog.Error("load failover configuration failed", "err", err)
			return fmt.Errorf("load failover configuration: %w", err)
		}
		if err := watchdog.FailoverRun(cfg); err != nil {
			slog.Error("watchdog failover failed", "err", err)
			return fmt.Errorf("watchdog failover: %w", err)
		}
		return nil
	}
	command.AddCommand(failover)
	return command
}

func newIfmgrCommand() *cobra.Command {
	command := leafCommand("ifmgr", "Run the interface manager for a configured role.", "  mwan ifmgr --role wan --config /etc/mwan/config.toml", cobra.NoArgs)
	var path string
	var options ifmgrFlags
	configFlag(command, &path)
	flags := command.Flags()
	roles := ifmgr.KnownRoles()
	slices.Sort(roles)
	flags.StringVar(&options.role, "role", "", "Override ifmgr.role from TOML; choose "+strings.Join(roles, ", ")+".")
	flags.BoolVar(&options.debug, "debug", false, "Enable DEBUG logging.")
	flags.BoolVar(&options.dryRun, "dry-run", false, "Record the requested mode in startup logs; network operations still execute.")
	flags.StringVar(&options.captureLegacyNPT, "capture-legacy-npt", "", "Capture original NPT producer state in this absolute manifest path.")
	flags.StringVar(&options.adoptLegacyNPT, "adopt-legacy-npt", "", "Adopt captured NPT receipts after the original producer stops, then exit.")
	flags.IntVar(&options.producerPID, "producer-pid", 0, "Identify the original WAN service process for capture.")
	flags.StringVar(&options.producerSHA256, "producer-sha256", "", "Identify the verified original release SHA256 for capture.")
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if options.role != "" && !slices.Contains(roles, options.role) {
			return fmt.Errorf("unknown ifmgr role %q; choose %s", options.role, strings.Join(roles, ", "))
		}
		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		if options.role == "wan" && options.captureLegacyNPT == "" && options.adoptLegacyNPT == "" {
			if err := bootstrapWANFirewall(current.Context(), networkjson.DefaultPath); err != nil {
				slog.Error("ifmgr firewall bootstrap failed", "err", err)
				return fmt.Errorf("ifmgr: %w", err)
			}
		}
		return withConfig(current, path, func(cfg *config.Config) error { return runIfMgrCommand(cfg, options) })
	}
	return command
}

func newHealthCommand() *cobra.Command {
	command := leafCommand("health", "Continuously probe IPv4, IPv6, and HTTP connectivity.", "  mwan health", cobra.NoArgs)
	command.RunE = func(_ *cobra.Command, _ []string) error {
		if err := healthcheck.Run(); err != nil {
			slog.Error("health command failed", "err", err)
			return fmt.Errorf("health: %w", err)
		}
		return nil
	}
	return command
}

func newNotifyCommand() *cobra.Command {
	command := leafCommand("notify", "Send or preview a notification self-test.", "  mwan notify --self-test --dry-run --config /etc/mwan/config.toml", cobra.NoArgs)
	var path, level string
	var options notifyFlags
	configFlag(command, &path)
	flags := command.Flags()
	flags.BoolVar(&options.selfTest, "self-test", false, "Emit a notification self-test.")
	flags.BoolVar(&options.dryRun, "dry-run", false, "Print the planned event without sending it.")
	flags.StringVar(&level, "level", "ERROR", "Set the event level: DEBUG, INFO, WARN, or ERROR.")
	flags.StringVar(&options.kind, "kind", "self-test", "Set the notification event kind.")
	flags.StringVar(&options.key, "key", "", "Set the notification event key (default: hostname).")
	flags.StringVar(&options.message, "message", defaultSelfTestMessage(), "Set the notification summary.")
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if !options.selfTest {
			return fmt.Errorf("--self-test is required")
		}
		parsed, err := parseLevel(level)
		if err != nil {
			return err
		}
		options.level = parsed
		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		if !flags.Changed("key") {
			options.key = defaultSelfTestKey()
		}
		return withConfig(current, path, func(cfg *config.Config) error {
			return runNotifyOptions(cfg, options, current.OutOrStdout(), current.ErrOrStderr())
		})
	}
	return command
}

func newPDCommand() *cobra.Command {
	command := leafCommand("pd <iface>", "Print the current delegated IPv6 prefix for an interface.", "  mwan pd enwan0", requiredArguments(1))
	command.Long = "Print the current delegated IPv6 prefix for an interface. iface must contain the Linux interface name. The command reads networkd lease state and prints one CIDR prefix."
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandResult(runPDProbe(arguments[0], current.OutOrStdout(), current.ErrOrStderr()))
	}
	return command
}

func newTraceCommand() *cobra.Command {
	command := leafCommand("trace-boot", "Write and print the current boot trace ID.", "  mwan trace-boot", cobra.NoArgs)
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandResult(runTraceBoot(current.OutOrStdout(), current.ErrOrStderr()))
	}
	return command
}

func newVersionCommand() *cobra.Command {
	command := leafCommand("version", "Print the compiled build and linked library versions.", "  mwan version", cobra.NoArgs)
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandResult(runVersion(current.OutOrStdout()))
	}
	return command
}

func newGatewayStatusCommand() *cobra.Command {
	command := leafCommand("gateway-status", "Print gateway health as JSON.", "  mwan gateway-status --config /etc/mwan/config.toml", cobra.NoArgs)
	var path string
	var options gatewayStatusFlags
	configFlag(command, &path)
	networkFlags(command, &options.networkPath, &options.schemaDir)
	command.RunE = func(current *cobra.Command, _ []string) error {
		return withConfig(current, path, func(cfg *config.Config) error {
			return commandResult(runGatewayStatusOptions(options, cfg, current.OutOrStdout(), current.ErrOrStderr()))
		})
	}
	return command
}

func newConfigStateCommand() *cobra.Command {
	command := leafCommand("config-state", "Print configuration file hashes and deploy state as JSON.", "  mwan config-state --config /etc/mwan/config.toml", cobra.NoArgs)
	var path string
	var options configStateFlags
	configFlag(command, &path)
	command.Flags().StringVar(&options.root, "root", "", "Read configuration files below this directory.")
	command.RunE = func(current *cobra.Command, _ []string) error {
		return withConfig(current, path, func(cfg *config.Config) error {
			return commandResult(runConfigStateOptions(options, cfg, current.OutOrStdout(), current.ErrOrStderr()))
		})
	}
	return command
}

func networkFlags(command *cobra.Command, path, schema *string) {
	command.Flags().StringVar(path, "network", networkjson.DefaultPath, "Read this WANCONFIG document.")
	command.Flags().StringVar(schema, "schema-dir", networkjson.DefaultSchemaDir, "Validate with YANG models from this directory.")
}

func newReportCommand() *cobra.Command {
	command := leafCommand("report", "Collect measurements and send network reports.", "  mwan report send --config /etc/mwan/config.toml", cobra.NoArgs)
	var path string
	addConfigFlag(command.PersistentFlags(), &path)
	for _, action := range []string{"watch", "send"} {
		var options reportFlags
		options.action = action
		description := "Send a summary from saved observations."
		if action == "watch" {
			description = "Collect network measurements and send scheduled reports and incident emails until cancellation."
		}
		child := leafCommand(action, description, "  mwan report "+action+" --config /etc/mwan/config.toml", cobra.NoArgs)
		networkFlags(child, &options.networkPath, &options.schemaDir)
		child.RunE = func(current *cobra.Command, _ []string) error {
			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			return withConfig(current, path, func(cfg *config.Config) error {
				return commandResult(runReportOptions(ctx, options, cfg, current.OutOrStdout(), current.ErrOrStderr()))
			})
		}
		command.AddCommand(child)
	}
	command.RunE = missingAction
	return command
}

func missingAction(command *cobra.Command, _ []string) error {
	command.SilenceUsage = false
	return fmt.Errorf("%s requires a command", command.CommandPath())
}

func newSelftestCommand() *cobra.Command {
	command := leafCommand("wanconfig-selftest", "Exercise the WANCONFIG datastore and notifications.", "  mwan wanconfig-selftest --repository /tmp/mwan-selftest --models-dir /usr/local/share/wanconfig/yang", cobra.NoArgs)
	var options selftestFlags
	command.Flags().StringVar(&options.repository, "repository", "", "Create a private sysrepo repository in this directory.")
	command.Flags().StringVar(&options.modelsDir, "models-dir", "", "Install YANG models from this directory in the private repository.")
	command.PreRunE = func(_ *cobra.Command, _ []string) error {
		if (options.repository == "") != (options.modelsDir == "") {
			return fmt.Errorf("--repository and --models-dir must be supplied together")
		}
		return nil
	}
	command.RunE = func(_ *cobra.Command, _ []string) error {
		return commandResult(runWanconfigSelftestOptions(slog.Default(), options))
	}
	return command
}
