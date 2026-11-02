package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/pd"
)

// runPDProbe prints the current DHCPv6-PD delegated prefix for an
// interface, one CIDR to stdout. It is the Go replacement for
// find-pd-prefixes.sh: it reads the live systemd-networkd lease over
// D-Bus, with networkctl and kernel-route fallbacks. Diagnostics go to stderr
// so stdout contains only the prefix for callers that read the command output.
func runPDProbe(iface string, output, diagnostics io.Writer) int {
	logger, closer := logging.New(logging.Config{Handlers: []slog.Handler{slog.NewTextHandler(diagnostics, &slog.HandlerOptions{Level: slog.LevelWarn})}})
	defer closer.Close()

	prefix, ok, err := pd.New(logger).Prefix(context.Background(), iface)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan pd: %s: %v\n", iface, err)
		return 1
	}
	if !ok {
		fmt.Fprintf(diagnostics, "mwan pd: no delegated prefix found for %s\n", iface)
		return 1
	}

	fmt.Fprintln(output, prefix.String())
	return 0
}
