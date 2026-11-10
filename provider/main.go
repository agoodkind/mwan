// Command terraform-provider-mwan serves deployment data through the OpenTofu protocol.
// It does not install files or manage services.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"goodkind.io/mwan/internal/version"
	"goodkind.io/mwan/provider/internal/provider"
)

const providerAddress = "tofu.home.arpa/agoodkind/mwan"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers")
	flag.Parse()

	buildCommit, stamped := version.StampedCommit()
	if !stamped {
		slog.Warn("provider build has no stamped commit; mwan_release rejects every version")
		buildCommit = ""
	}
	options := providerserver.ServeOpts{
		Address: providerAddress,
		Debug:   debug,
	}
	slog.Info("provider server starting", "address", providerAddress, "commit", buildCommit, "debug", debug)
	if err := providerserver.Serve(context.Background(), provider.New(buildCommit), options); err != nil {
		slog.Error("provider server stopped", "address", providerAddress, "err", err)
		os.Exit(1)
	}
}
