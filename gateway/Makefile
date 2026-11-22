################################################################################
# Settings
################################################################################

.DEFAULT_GOAL := help

ifeq ($(MWAN_CONTAINER),1)

################################################################################
# Build engine
################################################################################

BINARY     := mwan
CMD        := ./cmd/$(BINARY)
GKLOG_VPKG := goodkind.io/gklog/version
DIST_DIR   := dist
LOCAL_BIN  := bin

GO_BUILD_EXTRA_FLAGS := -trimpath -buildvcs=false
GO_MK_MODULES        := go-build.mk go-release.mk
GO_MK_PLATFORMS      := linux/$(WANCONFIG_DOCKER_ARCH)

RELEASE_PLATFORMS ?= linux/amd64 linux/arm64
RELEASE_BINS      := mwan:$(CMD):cgo=1,platforms=linux/amd64,platforms=linux/arm64

GOVULNCHECK_INSTALL := golang.org/x/vuln/cmd/govulncheck@v1.9.0

export GO_MK_CGO_OPTIONAL := github.com/osrg/gobgp/v4/internal/pkg/table

YANG_SCHEMA_DIR ?= internal/yangpub/schema
YANG_DIR        ?= yang
YANGLINT        ?= yanglint
YANG_MODELS     := $(wildcard $(YANG_SCHEMA_DIR)/*.yang)
YANG_INSTANCES  := $(wildcard $(YANG_DIR)/instances/*.json)

PROTO_OUTPUTS := \
	gen/mwan/v1/mwan.pb.go \
	gen/mwan/v1/mwan_grpc.pb.go
PROTO_INPUTS := $(wildcard proto/mwan/v1/*.proto) buf.yaml buf.gen.yaml

BPF_OUTPUT      := internal/ifmgr/modules/npt/bpf/npt_bpfel.o
NPT_BPF_INCLUDE ?= /usr/include/$(shell $(CC) -dumpmachine)

include bootstrap.mk

################################################################################
# Release and packaging
################################################################################

release: export GOFLAGS := -tags=osusergo,netgo$(if $(strip $(GOFLAGS)), $(GOFLAGS))
release: export CGO_LDFLAGS := -static

.PHONY: build-wanconfig

build-wanconfig: build
	mkdir -p $(LOCAL_BIN)
	cp $(DIST_BIN) $(LOCAL_BIN)/mwan-wanconfig-linux-$(WANCONFIG_DOCKER_ARCH)

################################################################################
# YANG validation
################################################################################

.PHONY: check-yang yang-validate yang-validate-instances

yang-validate:
	$(if $(strip $(YANG_MODELS)),,$(error \
		yang-validate: no model files match $(YANG_SCHEMA_DIR)/*.yang; \
		restore them or fix YANG_SCHEMA_DIR))
	$(YANGLINT) --version
	$(YANGLINT) $(YANG_MODELS)

yang-validate-instances:
	$(if $(strip $(YANG_INSTANCES)),,$(error \
		yang-validate-instances: no instance documents match $(YANG_DIR)/instances/*.json; \
		restore them or fix YANG_DIR))
	@for instance in $(YANG_INSTANCES); do \
		echo "yanglint -t config $$instance"; \
		$(YANGLINT) -t config $(YANG_MODELS) "$$instance" || exit 1; \
	done

check-yang: yang-validate yang-validate-instances

################################################################################
# Code generation
################################################################################

.PHONY: proto npt-bpf

proto: $(PROTO_OUTPUTS)

$(PROTO_OUTPUTS) &: $(PROTO_INPUTS)
	buf generate

npt-bpf: $(BPF_OUTPUT)

$(BPF_OUTPUT): internal/ifmgr/modules/npt/bpf/npt.c
	clang -O2 -g -Wall -Werror -target bpf -c internal/ifmgr/modules/npt/bpf/npt.c \
		-o $(BPF_OUTPUT) -I$(NPT_BPF_INCLUDE)

################################################################################
# Maintenance
################################################################################

.PHONY: tidy govulncheck-strict

tidy:
	go mod tidy

govulncheck-strict:
	go run $(GOVULNCHECK_INSTALL) ./...

else

################################################################################
# Docker settings
################################################################################

include ../build/wanconfig.mk
export $(filter WANCONFIG_%,$(.VARIABLES))

HOST_ARCH                := $(shell uname -m)
WANCONFIG_DOCKER_ARCH     ?= $(if \
	$(filter arm64 aarch64,$(HOST_ARCH)),arm64,amd64)
WANCONFIG_DOCKER_SOURCE   := $(abspath ..)
WANCONFIG_BUILDER_IMAGE   ?= mwan-wanconfig-builder:$(WANCONFIG_DOCKER_ARCH)
COMPOSE     := docker compose
HELP_SCRIPT := tools/help.awk

.PHONY: help

help: ## Show local targets, cache limits, and shared build help.
	@awk -f $(HELP_SCRIPT) $(firstword $(MAKEFILE_LIST))
	@printf '\nCache limits: MWAN_GOMOD_LIMIT_MB=%s MWAN_GO_BUILD_LIMIT_MB=%s MWAN_GO_MAKEFILE_LIMIT_MB=%s MWAN_BUILDKIT_LIMIT=%s\n\n' \
		'$(MWAN_GOMOD_LIMIT_MB)' '$(MWAN_GO_BUILD_LIMIT_MB)' '$(MWAN_GO_MAKEFILE_LIMIT_MB)' '$(MWAN_BUILDKIT_LIMIT)'
	$(BUILD_RUN) build make help

################################################################################
# Build and quality
################################################################################

# The CI overlay mounts GITHUB_OUTPUT for build services.
BUILD_FILES := -f compose.build.yaml $(if $(GITHUB_OUTPUT),-f compose.ci.yaml)
BUILD       := $(COMPOSE) $(BUILD_FILES)
BUILD_RUN   := $(BUILD) run --build --rm

export GO_MK_API_REPO GO_MK_API_REF
export YANG_SCHEMA_DIR YANG_DIR YANGLINT

.PHONY: build build-check

build: ## Build the gateway in the Linux builder.
	$(BUILD_RUN) build

build-check: ## Run the shared quality checks in the Linux builder.
	$(BUILD_RUN) check

################################################################################
# Tests
################################################################################

TEST := $(COMPOSE) -f compose.test.yaml

.PHONY: test test-protocol-namespace

test: ## Run the ordinary Go test suite in Docker.
	$(TEST) run --build --rm test

PROTOCOL_TEST ?=

test-protocol-namespace: ## Run the protocol namespace test suite in Docker.
	@set -e; \
	trap '$(TEST) down --remove-orphans' EXIT; \
	$(TEST) run --build --rm protocol-namespace go test -v -count=1 -timeout=30m -tags protocol $(if $(PROTOCOL_TEST),-run $(PROTOCOL_TEST)) ./internal/commandline

################################################################################
# Release and packaging
################################################################################

WANCONFIG_STACK_ARCH ?= $(or \
	$(GO_MK_TARGET_GOARCH),\
	$(lastword $(subst /, ,$(firstword $(RELEASE_PLATFORMS)))),\
	$(WANCONFIG_DOCKER_ARCH))
STACK := $(COMPOSE) -f compose.stack.yaml
export RELEASE_STAGE RELEASE_TAG RELEASE_PLATFORMS
export GO_MK_TARGET_GOOS GO_MK_TARGET_GOARCH

.PHONY: release wanconfig-stack-bundle

ifeq ($(RELEASE_STAGE),compile)
release: wanconfig-stack-bundle
endif

release: ## Run the selected release stage in the Linux builder.
	$(BUILD_RUN) release

wanconfig-stack-bundle: ## Build and verify the WANCONFIG stack bundle.
	$(STACK) run --build --rm stack-build

################################################################################
# Shared CI integration
################################################################################

ENGINE_TARGETS := \
	go-mk-ci-job-layout \
	go-mk-platforms \
	ci-changed \
	go-mk-cache-manifest \
	go-mk-prepare-submodules \
	go-mk-generate \
	go-mk-golangci-cache-save-decision \
	go-mk-release-assets

export GO_MK_DIFF_BASE GO_MK_DIFF_BASE_IS_EXACT
export GO_MK_DIFF_HEAD GO_MK_DEFAULT_BRANCH
export GO_MK_EVENT_NAME GO_MK_REF_NAME GO_MK_JOB_LAYOUT
export GO_MK_CI_PRIMARY_COMMAND GO_MK_CI_PRIMARY_OUTCOME
export GO_MK_CI_GOLANGCI_RAN GO_MK_CI_REPORT_GOLANGCI_RUN
export GO_MK_CI_LINT_CACHE_HIT GO_MK_CI_LINT_CACHE_PATHS
export GH_TOKEN GITHUB_OUTPUT GITHUB_ACTIONS GITHUB_REPOSITORY
export GITHUB_RUN_NUMBER GITHUB_RUN_ID GITHUB_SHA
export GITHUB_REF GITHUB_REF_NAME GITHUB_EVENT_NAME GITHUB_WORKSPACE

.PHONY: $(ENGINE_TARGETS)

$(ENGINE_TARGETS):
	$(BUILD_RUN) build make $@

################################################################################
# Cache management
################################################################################

override WANCONFIG_CACHE_ID := $(shell \
	printf '%s' '$(abspath ..)' | cksum | cut -d' ' -f1)
override MWAN_BUILDKIT_BUILDER := mwan-$(WANCONFIG_CACHE_ID)

MWAN_GOMOD_LIMIT_MB       ?= 1024
MWAN_GO_BUILD_LIMIT_MB    ?= 3072
MWAN_GO_MAKEFILE_LIMIT_MB ?= 3072
MWAN_BUILDKIT_LIMIT       ?= 8GB

CACHE_SCRIPT := tools/cache/volumes.sh

ifeq ($(MWAN_PRIVATE_BUILDER),1)
BUILDX_BUILDER := $(MWAN_BUILDKIT_BUILDER)
endif

.PHONY: cache-usage cache-trim cache-clean
.PHONY: cache-buildkit-init cache-buildkit-usage cache-buildkit-trim cache-buildkit-clean

cache-usage: ## List Docker cache volumes for this checkout and architecture.
cache-trim: ## Delete unused cache volumes that exceed their configured limits in MB.
cache-clean: ## Delete all unused cache volumes for this checkout and architecture.

cache-usage cache-trim cache-clean:
	@bash $(CACHE_SCRIPT) $@ '$(WANCONFIG_CACHE_ID)' '$(WANCONFIG_DOCKER_ARCH)' \
		'$(MWAN_GOMOD_LIMIT_MB)' '$(MWAN_GO_BUILD_LIMIT_MB)' '$(MWAN_GO_MAKEFILE_LIMIT_MB)'

cache-buildkit-init: ## Create a builder for this checkout; set MWAN_PRIVATE_BUILDER=1 to use it.
	docker buildx create --driver docker-container --driver-opt default-load=true --name '$(MWAN_BUILDKIT_BUILDER)'

cache-buildkit-usage: ## Report disk usage for this checkout's builder.
	docker buildx du --builder '$(MWAN_BUILDKIT_BUILDER)'

cache-buildkit-trim: ## Reduce this checkout's builder cache to MWAN_BUILDKIT_LIMIT.
	docker buildx prune --builder '$(MWAN_BUILDKIT_BUILDER)' --all --max-used-space '$(MWAN_BUILDKIT_LIMIT)'

cache-buildkit-clean: ## Delete unused build cache for this checkout's builder.
	docker buildx prune --builder '$(MWAN_BUILDKIT_BUILDER)' --all

################################################################################
# Compose environment
################################################################################

COMPOSE_ENV_VARS := \
	$(if $(filter 1,$(MWAN_PRIVATE_BUILDER)),BUILDX_BUILDER) \
	WANCONFIG_DOCKER_ARCH \
	WANCONFIG_DOCKER_SOURCE \
	WANCONFIG_BUILDER_IMAGE \
	WANCONFIG_STACK_ARCH \
	WANCONFIG_CACHE_ID \
	WANCONFIG_LIBYANG_VERSION \
	WANCONFIG_SYSREPO_VERSION \
	WANCONFIG_LIBYANG_COMMIT \
	WANCONFIG_SYSREPO_COMMIT \
	WANCONFIG_LIBYANG_CPP_VERSION \
	WANCONFIG_SYSREPO_CPP_VERSION \
	WANCONFIG_NGHTTP2_ASIO_VERSION \
	WANCONFIG_ROUSETTE_VERSION \
	WANCONFIG_LIBYANG_CPP_COMMIT \
	WANCONFIG_SYSREPO_CPP_COMMIT \
	WANCONFIG_NGHTTP2_ASIO_COMMIT \
	WANCONFIG_ROUSETTE_COMMIT

export $(COMPOSE_ENV_VARS)

.PHONY: apply-compose-env

apply-compose-env: ## Print architecture, paths, and dependency pins for direct Compose commands.
	@printf '%s\n' $(foreach name,$(COMPOSE_ENV_VARS),'$(name)=$($(name))')

endif
