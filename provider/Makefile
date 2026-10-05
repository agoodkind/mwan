# `make help` is the canonical source of truth for every target this repo
# supports. Developers run it before adding a target. Lint, build, test,
# deadcode, release, baseline, and service-install live in the central
# go-makefile pipeline fetched at parse time. Project-local lint, deadcode,
# audit, fmt, vet, and staticcheck targets are prohibited here, because they
# duplicate the central pipeline and let agents bypass strict rules.

# go-makefile stamps the release tag into gklog's version package,
# which the provider compares with the release version a configuration asks for.
BINARY     := terraform-provider-mwan
CMD        := .
GKLOG_VPKG := goodkind.io/gklog/version

# The provider links no C code. Every release target builds with cgo off.
export CGO_ENABLED := 0

GO_BUILD_EXTRA_FLAGS := -trimpath -buildvcs=false

RELEASE_PLATFORMS ?= darwin/arm64 linux/amd64 linux/arm64
RELEASE_BINS      := $(BINARY):$(CMD):platforms=darwin/arm64,platforms=linux/amd64,platforms=linux/arm64

GO_MK_MODULES := go-build.mk go-release.mk

# The provider imports packages from the goodkind.io/mwan module in ../gateway.
# go.mk writes a gitignored go.work from these paths before every build,
# because the shared lint rejects a local replace directive in go.mod.
GO_MK_WORKSPACE_USE := . ../gateway

# bootstrap.mk fetches go.mk + golangci.yml + every module in GO_MK_MODULES
# at parse time and -includes them.
include bootstrap.mk

.DEFAULT_GOAL := check
