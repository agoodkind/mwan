# These Make targets dispatch gateway builds, tests, and releases to Linux
# Compose services. For example, test-ovs runs the ovs service to exercise
# bridge setup and WAN packet forwarding.

################################################################################
# Settings
################################################################################

.DEFAULT_GOAL := help

################################################################################
# Docker settings
################################################################################

HOST_ARCH             := $(shell uname -m)
WANCONFIG_DOCKER_ARCH ?= $(if $(filter arm64 aarch64,$(HOST_ARCH)),arm64,amd64)

override WANCONFIG_CACHE_ID := $(shell \
	printf '%s' '$(abspath ..)' | cksum | cut -d' ' -f1)
export WANCONFIG_CACHE_ID

COMPOSE  := docker compose --env-file ../build/wanconfig.env -f compose.build.yaml
PLATFORM := $(COMPOSE) -f compose.$(WANCONFIG_DOCKER_ARCH).yaml
BUILD    := $(PLATFORM) $(if $(GITHUB_OUTPUT),-f compose.ci.yaml)
RUN      := $(PLATFORM) run --build --rm

.PHONY: help

help: ## List gateway commands.
	@awk -f tools/help.awk $(firstword $(MAKEFILE_LIST))

################################################################################
# Build and quality
################################################################################

.PHONY: build build-check

build: ## Build gateway in the Linux builder.
	$(BUILD) run --build --rm build

build-check: ## Run shared quality checks in Linux builder.
	$(BUILD) run --build --rm check

################################################################################
# Tests
################################################################################

.PHONY: test test-netns test-firewall test-ovs test-provider-drift
.PHONY: test-protocol-namespace test-systemd systemd-stop

test: ## Run ordinary Go test suite.
	$(RUN) $@

test-netns: ## Run network namespace tests.
test-firewall: ## Run firewall runtime tests.
test-ovs: ## Verify OVS bridge setup and WAN packet forwarding.
test-provider-drift: ## Compare installer with OpenTofu provider output.

test-netns test-firewall test-ovs test-provider-drift:
	$(RUN) $(@:test-%=%)

test-protocol-namespace: ## Run protocol namespace tests. Set PROTOCOL_TEST to select a test expression.
	@set -e; \
	trap '$(PLATFORM) down --remove-orphans' EXIT; \
	$(RUN) protocol-namespace

test-systemd: ## Run systemd tests and remove test environment afterward.
	@set -e; \
	trap '$(PLATFORM) down --remove-orphans' EXIT; \
	$(PLATFORM) up --build --wait systemd; \
	$(PLATFORM) exec -T systemd \
		go test -count=1 -timeout=20m -tags systemd ./integration/systemd

systemd-stop: ## Stop and remove this checkout's Compose containers and network.
	$(PLATFORM) down --remove-orphans

################################################################################
# YANG validation
################################################################################

.PHONY: check-yang yang-validate yang-validate-instances

check-yang: ## Validate YANG modules and instance documents.
yang-validate: ## Validate YANG modules.
yang-validate-instances: ## Validate YANG instance documents.

check-yang yang-validate yang-validate-instances:
	$(RUN) $@

################################################################################
# Release and packaging
################################################################################

WANCONFIG_STACK_ARCH ?= $(or \
	$(GO_MK_TARGET_GOARCH),\
	$(lastword $(subst /, ,$(firstword $(RELEASE_PLATFORMS)))),\
	$(WANCONFIG_DOCKER_ARCH))

.PHONY: release wanconfig-stack-bundle

ifeq ($(RELEASE_STAGE),compile)
release: wanconfig-stack-bundle
endif

release: ## Run selected release stage in Linux builder.
	$(BUILD) run --build --rm release

wanconfig-stack-bundle: ## Build and verify WANCONFIG stack bundle.
	$(COMPOSE) -f compose.$(WANCONFIG_STACK_ARCH).yaml run --build --rm stack-build

################################################################################
# Shared CI integration
################################################################################

# The shared release workflow calls these targets on the runner.
RELEASE_TARGETS := \
	go-mk-platforms \
	go-mk-cache-manifest \
	go-mk-prepare-submodules \
	go-mk-release-assets

.PHONY: $(RELEASE_TARGETS)

$(RELEASE_TARGETS):
	$(BUILD) run --build --rm build make -f bootstrap.mk $@

################################################################################
# Compose validation
################################################################################

.PHONY: compose-check

compose-check: ## Validate selected Compose configuration.
	$(BUILD) config --quiet
