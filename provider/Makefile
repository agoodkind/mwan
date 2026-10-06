# Run make help before adding a target.
# Use the shared go-makefile targets for builds, tests, lint, and releases.
# Local alternatives bypass the shared checks.

# go-makefile stamps the build commit into the provider.
# The release data source compares that commit with the requested tag's suffix.
BINARY     := terraform-provider-mwan
CMD        := .
GKLOG_VPKG := goodkind.io/gklog/version

# The provider does not require a C toolchain.
export CGO_ENABLED := 0

GO_BUILD_EXTRA_FLAGS := -trimpath -buildvcs=false

RELEASE_PLATFORMS ?= darwin/arm64 linux/amd64 linux/arm64
RELEASE_BINS      := $(BINARY):$(CMD):platforms=darwin/arm64,platforms=linux/amd64,platforms=linux/arm64

GO_MK_MODULES := go-build.mk go-release.mk

# The generated workspace resolves the gateway module dependency.
# The shared lint rules reject local replace directives.
GO_MK_WORKSPACE_USE := . ../gateway

include bootstrap.mk

.DEFAULT_GOAL := check
