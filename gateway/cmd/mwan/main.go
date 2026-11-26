// Package main starts the mwan command-line process.
package main

import (
	"os"

	"goodkind.io/mwan/gateway/internal/commandline"
	"goodkind.io/mwan/gateway/internal/logging"
	"goodkind.io/mwan/gateway/version"
)

func main() {
	logger, closer := logging.NewCommandLogger(os.Stderr, version.BuildVersionString())
	logger.Debug("mwan process boundary", "build", version.BuildVersionString())
	exitCode := commandline.RunProcess(os.Args, os.Stdin, os.Stdout, os.Stderr, logger, closer)
	os.Exit(exitCode)
}
