package commandline

// ovs-rule-status reads the configured bridge's counters from the
// ovs-vswitchd control socket. It writes one JSON result for an operator
// or monitoring tool to read.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/spf13/cobra"
	"goodkind.io/mwan/gateway/internal/commandline/commandui"
	"goodkind.io/mwan/gateway/internal/ovsreport"
	"goodkind.io/mwan/gateway/networkload"
)

func newOVSRuleStatus() *cobra.Command {
	cmd := commandui.Leaf(
		"ovs-rule-status CONTROL_SOCKET",
		"Show forwarding counters for each configured OpenFlow rule.",
		"  mwan ovs-rule-status /run/openvswitch/ovs-vswitchd.123.ctl --network /etc/mwan/network.json",
		cobra.ExactArgs(1),
	)
	var path, schema string
	commandui.NetworkFlags(cmd, &path, &schema)
	cmd.Long = "Read the configured bridge's rule counters from the ovs-vswitchd Unix control socket. " +
		"JSON output identifies each configured rule by name. " +
		"Forwarding reflects packet counts since rule installation or a counter reset."
	cmd.RunE = func(current *cobra.Command, args []string) error {
		settings, err := networkload.LoadOVS(path, schema)
		if err != nil {
			slog.ErrorContext(current.Context(), "OVS network document load failed", "err", err)

			return fmt.Errorf("load OVS network document: %w", err)
		}

		timeout := time.Duration(settings.TimeoutMillis) * time.Millisecond
		ctx, cancel := context.WithTimeout(current.Context(), timeout)
		defer cancel()
		rules, err := ovsreport.Read(ctx, args[0], settings)
		if err != nil {
			slog.ErrorContext(ctx, "OVS rule counter inspection failed", "err", err)

			return fmt.Errorf("inspect OVS forwarding: %w", err)
		}

		return json.NewEncoder(current.OutOrStdout()).Encode(rules)
	}

	return cmd
}
