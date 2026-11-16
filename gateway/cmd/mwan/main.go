package main

import (
	"log/slog"
	"os"

	"goodkind.io/mwan/internal/version"
)

func main() {
	os.Exit(
		run(
			os.Args[1:],
			os.Stdin,
			os.Stdout,
			os.Stderr,
			func(subcommand string) {
				slog.Info(
					"mwan boundary",
					"build",
					version.BuildVersionString(),
					"subcommand",
					subcommand,
				)
			},
		),
	)
}
