package commandline

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"goodkind.io/mwan/internal/logging"
	"goodkind.io/mwan/internal/pd"
	"goodkind.io/mwan/internal/version"
)

// runPDProbe prints a delegated prefix to output and errors to diagnostics.
func runPDProbe(iface string, output, diagnostics io.Writer) int {
	logger, closer := logging.New(logging.Config{
		BuildVersion: version.BuildVersionString(),
		Handlers: []slog.Handler{
			slog.NewTextHandler(diagnostics, &slog.HandlerOptions{Level: slog.LevelWarn}),
		},
	})
	defer closer.Close()

	prefix, found, err := pd.New(logger).Prefix(context.Background(), iface)
	if err != nil {
		fmt.Fprintf(diagnostics, "mwan pd: %s: %v\n", iface, err)

		return 1
	}

	if !found {
		fmt.Fprintf(diagnostics, "mwan pd: no delegated prefix found for %s\n", iface)

		return 1
	}

	fmt.Fprintln(output, prefix.String())

	return 0
}
