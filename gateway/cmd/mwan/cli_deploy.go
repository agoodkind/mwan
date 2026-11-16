package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/mwan/internal/config"
	"goodkind.io/mwan/internal/connectionid"
	"goodkind.io/mwan/internal/interfaceintent"
)

func newDeployCommand(name string) *cobra.Command {
	command := leafCommand(
		name,
		"Manage deployment operations and run deployment gates.",
		"  mwan "+name+" status operation-1 generation-1 --config /etc/mwan/config.toml",
		cobra.NoArgs,
	)
	command.RunE = missingAction

	for _, mode := range []operationMode{
		operationArm,
		operationWatch,
		operationStatus,
		operationLease,
		operationRelease,
		operationVerify,
		operationRecover,
	} {
		child := newDeployOperationCommand(name, mode)
		if name == "deploy-gate" && mode == operationVerify {
			child.Aliases = []string{deployGateCommitAlias}
		}

		command.AddCommand(child)
	}

	command.AddCommand(
		newCheckEgressCommand(),
		newCheckOwnedCommand(),
		newCheckNetworkCommand(),
		newCheckNetworkdCommand(),
		newCheckReleaseCommand(),
		newCheckFirewallCommand(),
		newInspectFirewallCommand(),
		newCheckFailoverCommand(),
		newWaitRebootCommand(),
		newWaitEgressCommand(),
		newWaitDeployCommand(),
	)

	return command
}

func newDeployOperationCommand(parent string, mode operationMode) *cobra.Command {
	arguments := " <operation_id> <generation>"
	count := 2
	description := "Inspect the deployment operation."
	exampleArguments := " operation-1 generation-1"
	long := "operation_id and generation must exactly match the saved deployment operation. The configured gateway VMID must match that operation."
	switch mode {
	case operationArm:
		arguments, count, description = " <manifest_path>", 1, "Arm a deployment operation from a manifest."
		exampleArguments = " /tmp/deploy-manifest.json"
		long = "manifest_path selects a deployment manifest JSON file. The manifest VMID must match the configured gateway. Supply --config explicitly with an absolute path to the runtime TOML configuration."
	case operationWatch:
		description = "Monitor the deployment operation and run recovery when required."
	case operationStatus:
	case operationLease:
		arguments, count, description = arguments+" <phase> <seconds>", 4, "Grant a mutation lease for the deployment operation."
		exampleArguments += " connection-handover 60"
		long += " phase identifies the mutation phase. seconds must contain a positive integer lease duration in seconds. The command prints the granted lease as JSON."
	case operationRelease:
		arguments, count, description = arguments+" <lease_id>", 3, "Release a mutation lease for the deployment operation."
		exampleArguments += " lease-1"
		long += " lease_id must exactly match the outstanding lease ID returned by the lease command."
	case operationVerify:
		description = "Verify and commit the deployment operation."
	case operationRecover:
		description = "Recover the deployment operation."
	}

	command := leafCommand(
		string(mode)+arguments,
		description,
		"  mwan "+parent+" "+string(mode)+exampleArguments+" --config /etc/mwan/config.toml",
		requiredArguments(count),
	)
	command.Long = description + "\n\n" + long
	var path string
	var options deployOperationOptions
	configFlag(command, &path)
	command.PreRunE = func(_ *cobra.Command, values []string) error {
		if mode == operationArm && !filepath.IsAbs(path) {
			return commandExitError{
				code: exitDeployGateUsage,
				cause: fmt.Errorf(
					"--config must explicitly select an absolute runtime configuration path",
				),
			}
		}

		parsed, err := parseDeployOperationOptions(mode, values, path)
		if err != nil {
			return err
		}

		options = parsed

		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		return commandResult(
			runDeployOperationOptions(options, current.OutOrStdout(), current.ErrOrStderr()),
		)
	}

	return command
}

func newCheckEgressCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeCheckEgress)+" <families> <probe_config_path>",
		"Check downstream egress for ipv4, ipv6, or ipv4,ipv6.",
		"  mwan deploy check-egress ipv4,ipv6 /etc/mwan/probes.json",
		requiredArguments(2),
	)
	command.Long = "Check downstream egress through the OPNsense guest. families accepts ipv4, ipv6, or ipv4,ipv6. probe_config_path selects one JSON object with opnsense_vmid and a source address and expected next hop for each required family: ipv4_source, ipv4_next_hop, ipv6_source, and ipv6_next_hop. Required addresses must contain unicast addresses of the selected family."
	var families requiredEgressFamilies
	var probe downstreamProbeConfig
	command.PreRunE = func(_ *cobra.Command, arguments []string) error {
		parsed, err := parseRequiredEgressFamilies(arguments[0])
		if err != nil {
			return err
		}

		families = parsed
		probe, err = readDownstreamProbeConfig(arguments[1], families)

		return err
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		deps := newDeployGateDeps(current.OutOrStdout(), current.ErrOrStderr())
		return commandResult(checkDownstreamEgress(current.Context(), deps, probe, families))
	}

	return command
}

func newCheckOwnedCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeCheckOwned),
		"Check mapped addresses on the gateway interfaces.",
		"  mwan deploy check-owned-addresses",
		cobra.NoArgs,
	)
	command.RunE = func(current *cobra.Command, _ []string) error {
		deps := newDeployGateDeps(current.OutOrStdout(), current.ErrOrStderr())
		return commandResult(checkOwnedAddresses(current.Context(), deps))
	}

	return command
}

func newCheckNetworkCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeCheckNetwork)+" <network_json> <schema_dir>",
		"Validate network intent and provider configuration.",
		"  mwan deploy check-network /etc/mwan/network.json /usr/local/share/wanconfig/yang",
		cobra.ExactArgs(2),
	)
	command.Long = "Validate network intent and provider configuration. network_json selects the WANCONFIG JSON document. schema_dir selects the YANG model directory."
	command.RunE = func(current *cobra.Command, arguments []string) error {
		deps := newDeployGateDeps(current.OutOrStdout(), current.ErrOrStderr())
		return commandResult(checkNetwork(deps, arguments[0], arguments[1]))
	}

	return command
}

func newCheckNetworkdCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeCheckNetworkd)+" <network_json> <schema_dir> <unit_dir>",
		"Compare networkd units with configured network intent.",
		"  mwan deploy check-networkd /etc/mwan/network.json /usr/local/share/wanconfig/yang /etc/systemd/network",
		cobra.ExactArgs(3),
	)
	command.Long = "Compare networkd units with configured network intent. network_json selects the WANCONFIG JSON document. schema_dir selects its YANG model directory. unit_dir selects the systemd-networkd unit directory to inspect."
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandResult(
			checkNetworkd(arguments[0], arguments[1], arguments[2], current.OutOrStdout()),
		)
	}

	return command
}

func newCheckReleaseCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeCheckRelease)+" <connection_id> <previous_owner>",
		"Verify connection release from networkd or mwan ownership.",
		"  mwan deploy check-release provider-1 networkd --config /etc/mwan/config.toml",
		requiredArguments(2),
	)
	command.Long = "Verify that a connection configured for external ownership has released its previous ownership receipts. connection_id selects the exact connection ID. previous_owner accepts networkd or mwan. A networkd release also requires the interface to report unmanaged state."
	var path string
	var id connectionid.ID
	var previous interfaceintent.Owner
	configFlag(command, &path)
	command.PreRunE = func(_ *cobra.Command, arguments []string) error {
		id, previous = connectionid.ID(arguments[0]), interfaceintent.Owner(arguments[1])
		if id == "" {
			return fmt.Errorf("connection ID is empty")
		}

		if previous != interfaceintent.OwnerNetworkd && previous != interfaceintent.OwnerMWAN {
			return fmt.Errorf("previous owner must be networkd or mwan")
		}

		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		cfg, err := config.LoadPath(path)
		if err != nil {
			fmt.Fprintf(current.OutOrStdout(), "release configuration rejected: %v\n", err)
			return commandResult(exitDeployGateFailed)
		}

		section, err := sharedWatchdogSection(cfg)
		if err != nil {
			fmt.Fprintf(current.OutOrStdout(), "release configuration rejected: %v\n", err)
			return commandResult(exitDeployGateFailed)
		}

		return commandResult(
			checkConnectionRelease(
				current.Context(),
				current.OutOrStdout(),
				cfg,
				section.ConnectivityTimeout(),
				id,
				previous,
			),
		)
	}

	return command
}

func newCheckFirewallCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeCheckFirewall)+" <network_json> <schema_dir>",
		"Apply and inspect firewall rules in a separate network namespace.",
		"  mwan deploy check-firewall /etc/mwan/network.json /usr/local/share/wanconfig/yang",
		cobra.ExactArgs(2),
	)
	var isolated bool
	command.Flags().BoolVar(
		&isolated,
		"isolated",
		false,
		"Run inside the caller-created network namespace.",
	)
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandResult(
			checkFirewall(
				arguments[0],
				arguments[1],
				isolated,
				current.OutOrStdout(),
				current.ErrOrStderr(),
			),
		)
	}

	return command
}

func newInspectFirewallCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeInspectFirewall)+" <network_json> <schema_dir>",
		"Inspect current firewall rules against network intent.",
		"  mwan deploy inspect-firewall /etc/mwan/network.json /usr/local/share/wanconfig/yang",
		cobra.ExactArgs(2),
	)
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandResult(
			inspectFirewall(
				arguments[0],
				arguments[1],
				current.OutOrStdout(),
				current.ErrOrStderr(),
			),
		)
	}

	return command
}

func newCheckFailoverCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeCheckFailover)+" <config.toml>",
		"Validate failover configuration and preconditions.",
		"  mwan deploy check-failover /etc/mwan/config.toml",
		cobra.ExactArgs(1),
	)
	command.Long = "Validate failover configuration and preconditions. config.toml selects the TOML configuration with the primary and failover guest identifiers and agent addresses."
	command.RunE = func(current *cobra.Command, arguments []string) error {
		return commandResult(checkFailover(arguments[0], current.ErrOrStderr()))
	}

	return command
}

func gateHost(command *cobra.Command, path string, execute func(deployGateDeps) int) error {
	cfg, err := config.LoadPath(path)
	if err != nil {
		slog.Error("deployment host configuration rejected", "err", err)
		return fmt.Errorf("load configuration: %w", err)
	}

	deps := gatewayGuestReads(newDeployGateDeps(command.OutOrStdout(), command.ErrOrStderr()), cfg)

	return commandResult(execute(deps))
}

func newWaitRebootCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeWaitReboot)+" <vmid> <old_boot_id> <seconds>",
		"Wait for a new guest boot ID; exit 2 when the guest remains unobservable.",
		"  mwan deploy wait-reboot 100 aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee 120 --config /etc/mwan/config.toml",
		requiredArguments(3),
	)
	command.Long = "Wait for a new guest boot ID. vmid must contain a positive guest ID. old_boot_id must contain the guest's previous lowercase boot UUID. seconds must contain a positive integer wait budget in seconds. Exit 0 means the boot ID changed. Exit 1 means the reboot did not occur. Exit 2 means the guest remained unobservable at the deadline."
	var path, oldBootID string
	var vmid int
	var budget time.Duration
	configFlag(command, &path)
	command.PreRunE = func(_ *cobra.Command, arguments []string) error {
		parsed, err := parseVMID(arguments[0])
		if err != nil {
			return err
		}

		vmid, oldBootID = parsed, arguments[1]
		if !bootIDPattern.MatchString(oldBootID) {
			return fmt.Errorf("old_boot_id must be a boot_id UUID")
		}

		budget, err = parseBudgetSeconds(arguments[2])

		return err
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		return gateHost(
			current,
			path,
			func(deps deployGateDeps) int {
				return waitReboot(current.Context(), deps, vmid, oldBootID, budget)
			},
		)
	}

	return command
}

func newWaitEgressCommand() *cobra.Command {
	command := leafCommand(
		string(gateModeWaitEgress)+" <seconds> <families> <consecutive_rounds>",
		"Wait for consecutive successful egress probes for ipv4, ipv6, or ipv4,ipv6.",
		"  mwan deploy wait-egress 60 ipv4,ipv6 3",
		requiredArguments(3),
	)
	command.Long = "Wait for consecutive successful egress probes. seconds must contain a positive integer wait budget in seconds. families accepts ipv4, ipv6, or ipv4,ipv6. consecutive_rounds must contain a positive integer count of consecutive successful rounds for every required family."
	var budget time.Duration
	var families requiredEgressFamilies
	var rounds int
	command.PreRunE = func(_ *cobra.Command, arguments []string) error {
		parsed, err := parseBudgetSeconds(arguments[0])
		if err != nil {
			return err
		}

		budget = parsed
		families, err = parseRequiredEgressFamilies(arguments[1])
		if err != nil {
			return err
		}

		rounds, err = parseConsecutiveRounds(arguments[2])

		return err
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		deps := newDeployGateDeps(current.OutOrStdout(), current.ErrOrStderr())
		return commandResult(waitEgress(current.Context(), deps, budget, families, rounds))
	}

	return command
}

func newWaitDeployCommand() *cobra.Command {
	command := leafCommand(
		string(
			gateModeWaitDeploy,
		)+" <vmid> <old_boot_id> <reboot_seconds> <egress_seconds> <trace_id> <verdict_path> <families> <consecutive_rounds> <probe_config_path>",
		"Record reboot, egress, and owned-address verdicts for a deployment.",
		"  mwan deploy wait-deploy 100 aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee 120 60 deploy-1 /tmp/verdict.json ipv4,ipv6 3 /etc/mwan/probes.json --config /etc/mwan/config.toml",
		requiredArguments(9),
	)
	command.Long = "Record reboot, egress, and owned-address verdicts for a deployment. Exit 0 confirms that the command wrote the verdict file. The JSON verdict fields report the gate outcomes.\n\nvmid must contain a positive guest ID.\nold_boot_id must contain the guest's previous lowercase boot UUID.\nreboot_seconds sets a positive integer reboot wait budget in seconds.\negress_seconds sets a positive integer egress wait budget in seconds.\ntrace_id identifies this deployment with 1 to 64 letters, digits, underscores, or hyphens.\nverdict_path selects the JSON verdict output file.\nfamilies accepts ipv4, ipv6, or ipv4,ipv6.\nconsecutive_rounds sets the positive integer count of required successful egress rounds.\nprobe_config_path selects downstream probe JSON with opnsense_vmid and the source and next-hop addresses for each required family."
	var path string
	var options waitDeployInputs
	configFlag(command, &path)
	command.PreRunE = func(_ *cobra.Command, arguments []string) error {
		parsed, err := parseWaitDeployArgs(arguments)
		if err != nil {
			return err
		}

		options = parsed

		return nil
	}
	command.RunE = func(current *cobra.Command, _ []string) error {
		return gateHost(
			current,
			path,
			func(deps deployGateDeps) int { return waitDeploy(current.Context(), deps, options) },
		)
	}

	return command
}
