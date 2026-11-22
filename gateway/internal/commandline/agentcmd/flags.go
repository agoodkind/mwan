package agentcmd

import (
	"github.com/spf13/pflag"

	"goodkind.io/mwan/internal/config"
)

const (
	socketPathFlag = "socket-path"
	tcpAddressFlag = "tcp-addr"
	logFileFlag    = "log-file"
	debugFlag      = "debug"
)

func bindAgentFlags(flags *pflag.FlagSet) *config.AgentSection {
	defaults := config.AgentDefaults()
	var settings config.AgentSection
	flags.StringVar(
		&settings.SocketPath,
		socketPathFlag,
		defaults.SocketPath,
		"Override agent.socket_path from TOML. The agent enables the Unix listener when the path is nonempty.",
	)
	flags.StringVar(
		&settings.TCPAddr,
		tcpAddressFlag,
		defaults.TCPAddr,
		"Override the TCP address in agent.tcp_addr.",
	)
	flags.StringVar(
		&settings.LogFile,
		logFileFlag,
		defaults.LogFile,
		"Override the JSON log path in agent.log_file.",
	)
	flags.BoolVar(
		&settings.Debug,
		debugFlag,
		defaults.Debug,
		"Override agent.debug. The agent enables gRPC reflection when true.",
	)

	return &settings
}

func agentOptions(flags *pflag.FlagSet, settings, configured config.AgentSection) config.AgentSection {
	if flags.Changed(socketPathFlag) {
		configured.SocketPath = settings.SocketPath
	}

	if flags.Changed(tcpAddressFlag) {
		configured.TCPAddr = settings.TCPAddr
	}

	if flags.Changed(logFileFlag) {
		configured.LogFile = settings.LogFile
	}

	if flags.Changed(debugFlag) {
		configured.Debug = settings.Debug
	}

	return configured
}
