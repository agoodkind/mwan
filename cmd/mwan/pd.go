package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"goodkind.io/mwan/internal/pd"
)

// runPDProbe prints the current DHCPv6-PD delegated prefix for an
// interface, one CIDR to stdout. It is the Go replacement for
// find-pd-prefixes.sh: it reads the live systemd-networkd lease over
// D-Bus, with the same networkctl, kernel-route, journal, and cached
// state-file fallbacks. Diagnostics go to stderr so stdout carries only
// the prefix, keeping the command pipe-friendly.
func runPDProbe(args []string) int {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: mwan pd <iface>")
		return 1
	}
	iface := args[0]

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelWarn,
	}))

	prefix, ok, err := pd.New(logger).Prefix(context.Background(), iface)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mwan pd: %s: %v\n", iface, err)
		return 1
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "mwan pd: no delegated prefix found for %s\n", iface)
		return 1
	}

	fmt.Println(prefix.String())
	return 0
}
