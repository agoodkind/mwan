# Run make help before adding a target.
# Use the shared go-makefile targets for builds, tests, lint, and releases.
# Local alternatives bypass the shared checks.

# go-makefile stamps the build commit into the provider.
# The release data source compares that commit with the requested tag's suffix.
BINARY     := terraform-provider-mwan
CMD        := .
GKLOG_VPKG := goodkind.io/gklog/version

GO_BUILD_EXTRA_FLAGS := -trimpath -buildvcs=false

RELEASE_PLATFORMS ?= darwin/arm64 linux/amd64 linux/arm64
RELEASE_BINS      := $(BINARY):$(CMD):platforms=darwin/arm64,platforms=linux/amd64,platforms=linux/arm64

GO_MK_MODULES := go-build.mk go-release.mk

# The generated workspace resolves the gateway module dependency.
# The shared lint rules reject local replace directives.
GO_MK_WORKSPACE_USE := . ../gateway

GO_MK_CGO_DEPS := yang

include bootstrap.mk
include ../build/yang.mk

GO_MK_CGO_CACHE_VERSIONS := $(YANG_NATIVE_CACHE_VERSIONS)
GO_MK_CGO_CACHE_INPUTS   := $(YANG_NATIVE_CACHE_INPUTS)

.DEFAULT_GOAL := check

# The override address must match providerAddress to select the test build.
TOFU                  ?= $(shell command -v tofu)
TOFU_PROVIDER_ADDRESS := tofu.home.arpa/agoodkind/mwan

# dev_overrides loads the temporary provider build without tofu init.
.PHONY: test-tofu-plan
test-tofu-plan: | $(GO_MK_PREREQS)
	@if [ -z "$(TOFU)" ]; then \
		echo "test-tofu-plan requires OpenTofu. Install tofu or set TOFU to its path." >&2; \
		exit 1; \
	fi
	@tofu_dir=$$(mktemp -d) || exit 1; \
	trap 'rm -rf "$$tofu_dir"' EXIT INT TERM; \
	go build $(GO_BUILD_FLAGS) -o "$$tofu_dir/$(BINARY)" $(CMD) || exit 1; \
	printf 'provider_installation {\n  dev_overrides {\n    "%s" = "%s"\n  }\n  direct {}\n}\n' \
		"$(TOFU_PROVIDER_ADDRESS)" "$$tofu_dir" > "$$tofu_dir/tofurc" || exit 1; \
	TOFU="$(TOFU)" TF_CLI_CONFIG_FILE="$$tofu_dir/tofurc" \
		go test -tags tofu -count=1 -run TestTofuPlan ./internal/provider

# PROVIDER_PACKAGE and TOFU must be absolute paths.
# PROVIDER_VERSION optionally sets the mirror version.
# Set ALLOW_MISSING_CHECKSUMS=1 to permit missing checksums.txt beside the archive.
# Set EXPECTED_COMMIT to require a matching provider commit.
RELEASE_PACKAGE_REQUIRED := test-release-package requires an absolute release archive path in PROVIDER_PACKAGE.
RELEASE_TOFU_REQUIRED    := test-release-package requires an absolute OpenTofu executable path in TOFU.

.PHONY: test-release-package
test-release-package:
	@if [ -z "$(PROVIDER_PACKAGE)" ]; then \
		echo "$(RELEASE_PACKAGE_REQUIRED)" >&2; \
		exit 1; \
	fi
	@if [ -z "$(TOFU)" ]; then \
		echo "$(RELEASE_TOFU_REQUIRED)" >&2; \
		exit 1; \
	fi
	@PROVIDER_PACKAGE="$(PROVIDER_PACKAGE)" TOFU="$(TOFU)" PROVIDER_VERSION="$(PROVIDER_VERSION)" \
		ALLOW_MISSING_CHECKSUMS="$(ALLOW_MISSING_CHECKSUMS)" EXPECTED_COMMIT="$(EXPECTED_COMMIT)" \
		scripts/test-release-package.sh
