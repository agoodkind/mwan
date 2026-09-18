# mwan Makefile.
#
# The build and lint pipeline is go-makefile, fetched at parse time by
# bootstrap.mk. Do not define lint, deadcode, audit, fmt, vet, or
# staticcheck targets here; `make help` lists the canonical entry points.
#
# Project-local targets: protobuf codegen, the YANG model gate, the cgo
# dependency recipes for the publishing binding, a docker lane for building
# the linux gateway binary on macOS, the wanconfig stack packaging, and a
# govulncheck wrapper that allowlists one known gobgp CVE.
#
# Shipped binaries come only from the CI release. The one cross build here is
# the packaging tool, which runs inside a container and never ships.

# ---------------------------------------------------------------------------
# Identity
# ---------------------------------------------------------------------------

# Build identity is gklog's. go-makefile stamps Commit, Dirty, and BuildTime
# into goodkind.io/gklog/version, so a released binary reports the commit it
# was cut from and a deploy can check it after the copy.
BINARY     := mwan
CMD        := ./cmd/$(BINARY)
GKLOG_VPKG := goodkind.io/gklog/version

# The release engine writes dist/<name>_<goos>_<goarch>/ and the build
# workflow uploads exactly that directory. bin/ holds only the docker lane's
# output.
DIST_DIR  := dist
LOCAL_BIN := bin

# cgo is never pinned here. Native builds keep cgo on and compile the yangpub
# binding.
GO_BUILD_EXTRA_FLAGS := -trimpath -buildvcs=false

# go-release.mk turns every shipped artifact into a signed, checksummed,
# attested GitHub release, which is what the deploy path installs.
GO_MK_MODULES := go-build.mk go-release.mk

# One linux binary, which links libyang and libsysrepo statically.
#
# RELEASE_PLATFORMS is a default, not an override. Each release job passes
# its own single platform in the environment, and this must yield to it.
# Forcing a list here made every job archive every platform, and release
# 202608161945-4-9244335 then failed verification on an unattested archive.
RELEASE_PLATFORMS ?= linux/amd64
RELEASE_BINS      := mwan:$(CMD):cgo=1,platforms=linux/amd64

# gobgp's only cgo symbol is a memory-reporting helper used solely by its
# tests.
export GO_MK_CGO_OPTIONAL := github.com/osrg/gobgp/v4/internal/pkg/table

# Every lint gate runs for the one shipped platform. mwan is almost entirely
# linux-tagged, so a darwin host pass would skip most of the package.
GO_MK_PLATFORMS := linux/amd64

# ---------------------------------------------------------------------------
# Wanconfig pins
# ---------------------------------------------------------------------------

# The six stack components are versioned here, not by the distribution, and
# this block is the only place they are pinned. The cgo hook builds libyang
# and sysrepo at these pins; the stack packaging builds all six, so a version
# change rebuilds the packages on the next release. nghttp2-asio has no
# release tags, so it pins a commit.
#
# libyang and sysrepo stay on the 3.x series: libyang-cpp v4 compiles against
# the libyang 3.x API only (5.x renamed print/parse symbols), and sysrepo
# v3.7.11 declares its libyang dependency as 3.13.x. The cpp bindings' ">="
# bounds state a floor, not upper compatibility.
#
# The gateway deploy fails unless the libsysrepo version the installed binary
# reports equals the inventory's wanconfig_sysrepo_soversion, so a sysrepo
# bump here needs that inventory value updated before the release deploys.
WANCONFIG_LIBYANG_VERSION      := v3.13.6
WANCONFIG_SYSREPO_VERSION      := v3.7.11
WANCONFIG_LIBYANG_CPP_VERSION  := v4
WANCONFIG_SYSREPO_CPP_VERSION  := v6
WANCONFIG_NGHTTP2_ASIO_VERSION := e877868abe
WANCONFIG_ROUSETTE_VERSION     := v2

# Each version pin carries the full commit hash its tag pointed at when it was
# reviewed. The packaging build refuses a tag that resolves elsewhere, so a
# force-moved upstream tag cannot change what a release packages. A version
# bump updates both lines together.
WANCONFIG_LIBYANG_COMMIT      := c2ddd01b9b810a30d6a7d6749a3bc9adeb7b01fb
WANCONFIG_SYSREPO_COMMIT      := 1b720b196f630f348d9e0c131d326b3fb8c6aca7
WANCONFIG_LIBYANG_CPP_COMMIT  := 249da7280864fbda5fccb340b455b7000ebfe67d
WANCONFIG_SYSREPO_CPP_COMMIT  := 01bed8d91bfb746c20cc53ae4e8d64e2c78d2a9e
WANCONFIG_NGHTTP2_ASIO_COMMIT := e877868abe06a83ed0a6ac6e245c07f6f20866b5
WANCONFIG_ROUSETTE_COMMIT     := 4685b3379b259bc1b4aca79c92ca47d2b1e48e5e

WANCONFIG_PINS := \
	$(WANCONFIG_LIBYANG_VERSION) \
	$(WANCONFIG_SYSREPO_VERSION) \
	$(WANCONFIG_LIBYANG_CPP_VERSION) \
	$(WANCONFIG_SYSREPO_CPP_VERSION) \
	$(WANCONFIG_NGHTTP2_ASIO_VERSION) \
	$(WANCONFIG_ROUSETTE_VERSION) \
	$(WANCONFIG_LIBYANG_COMMIT) \
	$(WANCONFIG_SYSREPO_COMMIT) \
	$(WANCONFIG_LIBYANG_CPP_COMMIT) \
	$(WANCONFIG_SYSREPO_CPP_COMMIT) \
	$(WANCONFIG_NGHTTP2_ASIO_COMMIT) \
	$(WANCONFIG_ROUSETTE_COMMIT)

# go-makefile's GO_MK_CGO_DEPS hook builds libyang and sysrepo into the
# per-os/arch prefix before every gate, so the cgo files stay fully checked
# on linux CI.
GO_MK_CGO_DEPS := libyang sysrepo

include bootstrap.mk

# The test gate runs the binary it just linked, which loads libyang and
# libsysrepo at runtime from the per-target prefix.
export LD_LIBRARY_PATH := $(GO_MK_CGO_PREFIX)/lib$(if $(strip $(LD_LIBRARY_PATH)),:$(LD_LIBRARY_PATH))

# Fully static release binary. These two values are the only way to reach
# the compiler and the external linker from the release engine's fixed
# build command. They attach to the release target only: a plain export
# would hand a host tool the target's link flags, and building
# golangci-lint on macOS then fails on a missing static libc.
release: export GOFLAGS := -tags=osusergo,netgo$(if $(strip $(GOFLAGS)), $(GOFLAGS))
release: export CGO_LDFLAGS := -static

.DEFAULT_GOAL := check

# ---------------------------------------------------------------------------
# Protobuf
# ---------------------------------------------------------------------------

# Requires buf, protoc-gen-go, protoc-gen-go-grpc on PATH.
BUF   ?= buf
GOBIN ?= $(shell go env GOPATH)/bin
export PATH := $(GOBIN):$(PATH)

.PHONY: proto
proto:
	@mkdir -p gen/mwan/v1
	$(BUF) generate

# ---------------------------------------------------------------------------
# YANG model gate
# ---------------------------------------------------------------------------

# yanglint validates the modules the gateway serves itself with. Install it
# with `brew install libyang` locally; CI uses the libyang2-tools package.
# The IETF modules are vendored under third_party/yang at the exact revisions
# this repository pins, and each gate names its files explicitly, so adding a
# second revision of a module never changes which one a gate parses.
YANG_DIR     ?= yang
YANG_RFC_DIR ?= third_party/yang/standard/ietf/RFC
YANGLINT     ?= yanglint

# Both gates resolve their inputs through make rather than a shell glob in the
# recipe, so an empty result is a value each gate can refuse. A shell glob that
# matches nothing expands to itself and reaches yanglint as a literal path,
# which fails for the wrong reason and names no cause.
YANG_MODELS := $(wildcard $(YANG_DIR)/*.yang)

YANG_IETF_MODULES := \
	$(YANG_RFC_DIR)/ietf-yang-types@2025-12-22.yang \
	$(YANG_RFC_DIR)/ietf-inet-types@2025-12-22.yang \
	$(YANG_RFC_DIR)/ietf-interfaces@2018-02-20.yang \
	$(YANG_RFC_DIR)/ietf-ip@2018-02-22.yang \
	$(YANG_RFC_DIR)/ietf-routing@2018-03-13.yang \
	$(YANG_RFC_DIR)/ietf-nat@2019-01-10.yang

.PHONY: yang-validate
yang-validate:
	@if [ -z "$(strip $(YANG_MODELS))" ]; then \
		echo "yang-validate: no model files match $(YANG_DIR)/*.yang; restore them or fix YANG_DIR" >&2; \
		exit 1; \
	fi
	$(YANGLINT) --version
	$(YANGLINT) $(YANG_IETF_MODULES) $(YANG_MODELS)

# A data instance carrying an interface must give it a type, and that identity
# lives in the interface-type registry, so the instance gate needs one module
# the schema gate does not.
YANG_INSTANCE_MODULES := \
	$(YANG_RFC_DIR)/ietf-yang-types@2025-12-22.yang \
	$(YANG_RFC_DIR)/ietf-inet-types@2025-12-22.yang \
	$(YANG_RFC_DIR)/iana-if-type@2014-05-08.yang \
	$(YANG_RFC_DIR)/ietf-interfaces@2018-02-20.yang \
	$(YANG_RFC_DIR)/ietf-ip@2018-02-22.yang \
	$(YANG_RFC_DIR)/ietf-routing@2018-03-13.yang \
	$(YANG_RFC_DIR)/ietf-nat@2019-01-10.yang

YANG_INSTANCES := $(wildcard $(YANG_DIR)/instances/*.json)

# Each instance is validated as configuration, the same check the deploy runs on
# the rendered file and the daemon runs at startup. A schema change that would
# reject the shape the gateway is configured with fails here.
#
# The loop body runs once per document, so an empty list would run it zero times
# and report success. This gate refuses that: a run that validated nothing is
# not a passing run.
.PHONY: yang-validate-instances
yang-validate-instances:
	@if [ -z "$(strip $(YANG_INSTANCES))" ]; then \
		echo "yang-validate-instances: no instance documents match $(YANG_DIR)/instances/*.json; restore them or fix YANG_DIR" >&2; \
		exit 1; \
	fi
	@for instance in $(YANG_INSTANCES); do \
		echo "yanglint -t config $$instance"; \
		$(YANGLINT) -t config $(YANG_INSTANCE_MODULES) $(YANG_MODELS) "$$instance" || exit 1; \
	done

check: yang-validate yang-validate-instances

# ---------------------------------------------------------------------------
# cgo dependencies for the publishing binding
# ---------------------------------------------------------------------------

# Each recipe clones its pinned tag, builds a static archive, installs it
# into the per-target prefix go.mk supplies, and stamps the version so a
# warm tree skips the rebuild.
#
# The sources live outside this Go module on purpose. A checkout inside the
# module makes the lint gates treat the tree as nested working trees, which
# turns a package absent on one platform into a hard error. They go under the
# user cache directory, below a copy of this checkout's path so two checkouts
# never clone into one tree, and nothing reads them after the install. The
# path is joined in make, so no shell quoting touches the checkout path.
WANCONFIG_CGO_SRC := $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/mwan/wanconfig-cgo-src/$(patsubst /%,%,$(CURDIR))

# sysrepo compiles its repository path into the library, so this must be
# the path the gateway uses. A build-directory prefix makes sr_connect fail
# on the gateway with "Operation not supported".
WANCONFIG_SYSREPO_REPO_PATH := /etc/sysrepo

# Only the archives and headers are wanted. libyang's own executables link
# against the static archive without naming pcre2 and libm, so building
# them fails the install.
WANCONFIG_LIBYANG_CMAKE_FLAGS := -DENABLE_TOOLS=OFF

# The static libyang archive needs pcre2 and libm after it. Its generated
# pkg-config file omits them, so the recipe writes them onto the Libs line
# and cgo picks them up through its ordinary pkg-config lookup.
WANCONFIG_LIBYANG_STATIC_LIBS := -lpcre2-8 -lm

# SR_HAVE_DLOPEN=OFF compiles sysrepo's external-plugin machinery out. The
# default build bakes the plugin directory's build-time prefix into the
# binary, and scanning it under the ifmgr unit's ProtectHome=true returned
# EACCES, so sr_connect failed with SR_ERR_SYS on the gateway.
#
# The group, umask, and NACM data mode match the upstream sysrepo Debian
# packaging (MWAN-435), so the static copy inside the mwan binary and the
# packaged libsysrepo7 agree on the ownership and modes of /etc/sysrepo
# and the shared memory. The daemon's unit joins group sysrepo.
WANCONFIG_SYSREPO_CMAKE_FLAGS := \
	-DREPO_PATH=$(WANCONFIG_SYSREPO_REPO_PATH) \
	-DSR_HAVE_DLOPEN=OFF \
	-DSYSREPO_UMASK=007 \
	-DSYSREPO_GROUP=sysrepo \
	-DNACM_SRMON_DATA_PERM=660 \
	-DENABLE_EXAMPLES=OFF \
	-DENABLE_SYSREPOCTL=OFF \
	-DENABLE_SYSREPOCFG=OFF \
	-DENABLE_SYSREPO_PLUGIND=OFF

# The stamps and the CI cache key carry a checksum of each dependency's
# flags, so a flag change at the same version rebuilds instead of reusing a
# stale archive.
WANCONFIG_LIBYANG_FLAGS_REV := $(shell printf '%s' '$(WANCONFIG_LIBYANG_CMAKE_FLAGS) $(WANCONFIG_LIBYANG_STATIC_LIBS)' | cksum | cut -d' ' -f1)
WANCONFIG_SYSREPO_FLAGS_REV := $(shell printf '%s' '$(WANCONFIG_SYSREPO_CMAKE_FLAGS)' | cksum | cut -d' ' -f1)

WANCONFIG_LIBYANG_STAMP := $(GO_MK_CGO_PREFIX)/.dep-libyang-$(WANCONFIG_LIBYANG_VERSION)-f$(WANCONFIG_LIBYANG_FLAGS_REV).stamp
WANCONFIG_SYSREPO_STAMP := $(GO_MK_CGO_PREFIX)/.dep-sysrepo-$(WANCONFIG_SYSREPO_VERSION)-f$(WANCONFIG_SYSREPO_FLAGS_REV).stamp

# The hook is skipped on darwin, where sysrepo cannot build because it needs
# robust pthread mutexes, and for a non-linux target, which has no cross
# toolchain and never links the binding.
ifeq ($(shell uname -s),Darwin)
WANCONFIG_CGO_SKIP := sysrepo needs robust pthread mutexes, absent on darwin
endif
ifneq ($(strip $(GO_MK_TARGET_GOOS)),)
ifneq ($(GO_MK_TARGET_GOOS),linux)
WANCONFIG_CGO_SKIP := no cross toolchain for $(GO_MK_TARGET_GOOS), and only the linux artifact links it
endif
endif

# wanconfig_cgo_configure NAME FLAGS: configure a static build of a clone.
define wanconfig_cgo_configure
	cmake -S $(WANCONFIG_CGO_SRC)/$(1) -B $(WANCONFIG_CGO_SRC)/$(1)/build \
		-DCMAKE_BUILD_TYPE=Release \
		-DCMAKE_INSTALL_PREFIX=$(GO_MK_CGO_PREFIX) \
		-DCMAKE_PREFIX_PATH=$(GO_MK_CGO_PREFIX) \
		-DCMAKE_C_FLAGS="$$(pkg-config --cflags-only-I libpcre2-8)" \
		-DBUILD_SHARED_LIBS=OFF \
		$(2) \
		-DBUILD_TESTING=OFF
endef

# wanconfig_cgo_install NAME: build the configured clone and copy its DESTDIR
# stage into the prefix. The install is staged because a CI runner cannot
# create the repository path directly.
define wanconfig_cgo_install
	cmake --build $(WANCONFIG_CGO_SRC)/$(1)/build --parallel
	DESTDIR=$(WANCONFIG_CGO_SRC)/stage-$(1) cmake --install $(WANCONFIG_CGO_SRC)/$(1)/build
	mkdir -p $(GO_MK_CGO_PREFIX)
	cp -R $(WANCONFIG_CGO_SRC)/stage-$(1)$(GO_MK_CGO_PREFIX)/. $(GO_MK_CGO_PREFIX)/
endef

.PHONY: go-mk-cgo-dep-libyang go-mk-cgo-dep-sysrepo

ifdef WANCONFIG_CGO_SKIP
go-mk-cgo-dep-libyang:
	@echo "cgo dep libyang skipped: $(WANCONFIG_CGO_SKIP)"

go-mk-cgo-dep-sysrepo:
	@echo "cgo dep sysrepo skipped: $(WANCONFIG_CGO_SKIP)"
else
go-mk-cgo-dep-libyang: $(WANCONFIG_LIBYANG_STAMP)
go-mk-cgo-dep-sysrepo: $(WANCONFIG_SYSREPO_STAMP)

$(WANCONFIG_LIBYANG_STAMP):
	rm -rf $(WANCONFIG_CGO_SRC)/libyang $(WANCONFIG_CGO_SRC)/stage-libyang
	git clone --depth 1 --branch $(WANCONFIG_LIBYANG_VERSION) https://github.com/CESNET/libyang.git $(WANCONFIG_CGO_SRC)/libyang
	$(call wanconfig_cgo_configure,libyang,$(WANCONFIG_LIBYANG_CMAKE_FLAGS))
	$(call wanconfig_cgo_install,libyang)
	awk -v extra="$(WANCONFIG_LIBYANG_STATIC_LIBS)" '/^Libs:/ {print $$0 " " extra; next} {print}' $(GO_MK_CGO_PREFIX)/lib/pkgconfig/libyang.pc > $(GO_MK_CGO_PREFIX)/lib/pkgconfig/libyang.pc.tmp
	mv $(GO_MK_CGO_PREFIX)/lib/pkgconfig/libyang.pc.tmp $(GO_MK_CGO_PREFIX)/lib/pkgconfig/libyang.pc
	touch $@

$(WANCONFIG_SYSREPO_STAMP): $(WANCONFIG_LIBYANG_STAMP)
	rm -rf $(WANCONFIG_CGO_SRC)/sysrepo $(WANCONFIG_CGO_SRC)/stage-sysrepo
	git clone --depth 1 --branch $(WANCONFIG_SYSREPO_VERSION) https://github.com/sysrepo/sysrepo.git $(WANCONFIG_CGO_SRC)/sysrepo
	$(call wanconfig_cgo_configure,sysrepo,$(WANCONFIG_SYSREPO_CMAKE_FLAGS))
	$(call wanconfig_cgo_install,sysrepo)
	touch $@
endif

# ---------------------------------------------------------------------------
# Docker lane for the linux gateway binary on macOS
# ---------------------------------------------------------------------------

# Builds the linux gateway binary inside a Debian trixie golang image that
# carries the pinned libyang and sysrepo, so the cgo binding links the
# gateway's library generation and glibc. Nothing deploys its output.
WANCONFIG_BUILDER_IMAGE := mwan-wanconfig-builder

# The image is built here and published nowhere, so `docker run` cannot pull
# it: every target that runs the image must build it first or fail on a clean
# checkout. Layer cache makes a repeat build a few seconds, and a changed
# Dockerfile still rebuilds.
.PHONY: wanconfig-builder-image
wanconfig-builder-image:
	docker build --platform linux/amd64 \
		--build-arg LIBYANG_VERSION=$(WANCONFIG_LIBYANG_VERSION) \
		--build-arg SYSREPO_VERSION=$(WANCONFIG_SYSREPO_VERSION) \
		--build-arg "LIBYANG_CMAKE_FLAGS=$(WANCONFIG_LIBYANG_CMAKE_FLAGS)" \
		--build-arg "SYSREPO_CMAKE_FLAGS=$(WANCONFIG_SYSREPO_CMAKE_FLAGS)" \
		-t $(WANCONFIG_BUILDER_IMAGE) tools/wanconfigbuilder

.PHONY: build-wanconfig
build-wanconfig: wanconfig-builder-image
	@mkdir -p $(LOCAL_BIN)
	docker run --rm --platform linux/amd64 \
		-v $(CURDIR):/src -w /src \
		-v mwan-wanconfig-gomod:/go/pkg/mod \
		-e GOWORK=off \
		$(WANCONFIG_BUILDER_IMAGE) \
		go build $(GO_BUILD_EXTRA_FLAGS) -ldflags='$(GO_BUILD_LDFLAGS)' -o $(LOCAL_BIN)/$(BINARY)-wanconfig $(CMD)

# On macOS the host test run is not the real one: darwin cannot build the
# cgo sysrepo binding, so every package that exercises the publishing
# binding compiles out and `go test ./...` passes while proving nothing
# about them. Route the whole suite through the same builder image the
# gateway binary uses, so a mac run and a CI run exercise identical code.
# The container mounts the repository root, which is the module root and also
# holds the YANG models the suite reads. The recipe below replaces go.mk's
# test target on darwin only, which is why make prints an "overriding
# commands" warning here.
ifeq ($(shell uname -s),Darwin)
.PHONY: test
test: wanconfig-builder-image
	@echo "darwin: running the suite in $(WANCONFIG_BUILDER_IMAGE), not on the host"
	docker run --rm --platform linux/amd64 \
		-v $(CURDIR):/src -w /src \
		-v mwan-wanconfig-gomod:/go/pkg/mod \
		-e GOWORK=off \
		$(WANCONFIG_BUILDER_IMAGE) \
		go test -count=1 ./...
endif

# ---------------------------------------------------------------------------
# Wanconfig management stack packages (MWAN-431)
# ---------------------------------------------------------------------------

# The six stack components ship as Debian packages built here and published
# with the mwan release, so a gateway installs them with apt and never
# compiles. The packaging tool is pure Go (tools/wanconfigstack). It is
# cross-built for linux and run inside a stock Debian trixie container, the
# only place the stack is ever compiled, so the packages carry trixie's
# shared-library ABI.
#
# The bundle lands in dist as wanconfig-stack_linux_<arch>.tar.gz. The
# release engine publishes, checksums, attests, and verifies every
# dist/*.tar.gz, so it rides the release unchanged.
#
# The built bundle is kept under the cgo prefix, which CI caches under a key
# that carries the stack pins, the tool sources, and the module files that
# pin the tool's dependencies. An unchanged stack is copied, not rebuilt.
#
# The architecture is amd64 unless the release engine names another: the
# gateway is amd64, and the upstream sysrepo deb template hardcodes amd64
# multiarch paths, so the local lane on an arm64 host runs the container
# under emulation rather than producing packages no gateway installs.
WANCONFIG_STACK_ARCH    ?= $(if $(strip $(GO_MK_TARGET_GOARCH)),$(GO_MK_TARGET_GOARCH),amd64)
WANCONFIG_STACK_IMAGE   := debian:trixie
WANCONFIG_STACK_SOURCES := $(wildcard tools/wanconfigstack/*.go) go.mod go.sum
WANCONFIG_STACK_BUNDLE  := wanconfig-stack_linux_$(WANCONFIG_STACK_ARCH).tar.gz
WANCONFIG_STACK_CACHE   := $(GO_MK_CGO_PREFIX)/wanconfig-stack
WANCONFIG_STACK_KEY     := $(shell printf '%s' '$(WANCONFIG_PINS) $(WANCONFIG_STACK_IMAGE)' | cat - $(WANCONFIG_STACK_SOURCES) | cksum | cut -d' ' -f1)
WANCONFIG_STACK_TOOL    := $(WANCONFIG_STACK_CACHE)/wanconfigstack-$(WANCONFIG_STACK_ARCH)
WANCONFIG_STACK_CACHED  := $(WANCONFIG_STACK_CACHE)/$(WANCONFIG_STACK_BUNDLE).$(WANCONFIG_STACK_KEY)

WANCONFIG_STACK_FLAGS := \
	-wanconfig_libyang_version $(WANCONFIG_LIBYANG_VERSION) \
	-wanconfig_sysrepo_version $(WANCONFIG_SYSREPO_VERSION) \
	-wanconfig_libyang_cpp_version $(WANCONFIG_LIBYANG_CPP_VERSION) \
	-wanconfig_sysrepo_cpp_version $(WANCONFIG_SYSREPO_CPP_VERSION) \
	-wanconfig_nghttp2_asio_version $(WANCONFIG_NGHTTP2_ASIO_VERSION) \
	-wanconfig_rousette_version $(WANCONFIG_ROUSETTE_VERSION) \
	-wanconfig_libyang_commit $(WANCONFIG_LIBYANG_COMMIT) \
	-wanconfig_sysrepo_commit $(WANCONFIG_SYSREPO_COMMIT) \
	-wanconfig_libyang_cpp_commit $(WANCONFIG_LIBYANG_CPP_COMMIT) \
	-wanconfig_sysrepo_cpp_commit $(WANCONFIG_SYSREPO_CPP_COMMIT) \
	-wanconfig_nghttp2_asio_commit $(WANCONFIG_NGHTTP2_ASIO_COMMIT) \
	-wanconfig_rousette_commit $(WANCONFIG_ROUSETTE_COMMIT)

GO_MK_CGO_CACHE_VERSIONS := \
	libyang=$(WANCONFIG_LIBYANG_VERSION)-f$(WANCONFIG_LIBYANG_FLAGS_REV) \
	sysrepo=$(WANCONFIG_SYSREPO_VERSION)-f$(WANCONFIG_SYSREPO_FLAGS_REV) \
	libyang-cpp=$(WANCONFIG_LIBYANG_CPP_VERSION) \
	sysrepo-cpp=$(WANCONFIG_SYSREPO_CPP_VERSION) \
	nghttp2-asio=$(WANCONFIG_NGHTTP2_ASIO_VERSION) \
	rousette=$(WANCONFIG_ROUSETTE_VERSION)
GO_MK_CGO_CACHE_INPUTS := $(WANCONFIG_STACK_SOURCES)

$(WANCONFIG_STACK_TOOL): $(WANCONFIG_STACK_SOURCES)
	@mkdir -p $(WANCONFIG_STACK_CACHE)
	GOOS=linux GOARCH=$(WANCONFIG_STACK_ARCH) CGO_ENABLED=0 go build $(GO_BUILD_EXTRA_FLAGS) -o $@ ./tools/wanconfigstack

# The bundle is built in one stock container and then proven in a second,
# fresh one: every package installs through apt, every shared library the
# stack binaries need resolves, and the datastore tool and the RESTCONF
# server start. A bundle that fails the proof is never cached.
#
# The tool is an order-only prerequisite: the cache key already hashes the
# tool sources, and a restored cache carries an older tool than the fresh
# checkout, so a timestamp dependency would rebuild the stack on every run.
$(WANCONFIG_STACK_CACHED): | $(WANCONFIG_STACK_TOOL)
	docker run --rm --platform linux/$(WANCONFIG_STACK_ARCH) \
		-v $(WANCONFIG_STACK_TOOL):/usr/local/bin/wanconfigstack:ro \
		-v $(WANCONFIG_STACK_CACHE):/out \
		$(WANCONFIG_STACK_IMAGE) \
		wanconfigstack $(WANCONFIG_STACK_FLAGS) -out /out
	docker run --rm --platform linux/$(WANCONFIG_STACK_ARCH) \
		-v $(WANCONFIG_STACK_TOOL):/usr/local/bin/wanconfigstack:ro \
		-v $(WANCONFIG_STACK_CACHE):/out:ro \
		$(WANCONFIG_STACK_IMAGE) \
		wanconfigstack -verify /out/$(WANCONFIG_STACK_BUNDLE)
	mv $(WANCONFIG_STACK_CACHE)/$(WANCONFIG_STACK_BUNDLE) $@

.PHONY: wanconfig-stack-bundle
wanconfig-stack-bundle: $(WANCONFIG_STACK_CACHED)
	@mkdir -p $(DIST_DIR)
	cp $< $(DIST_DIR)/$(WANCONFIG_STACK_BUNDLE)

# Re-run the proof against the cached bundle on demand.
.PHONY: wanconfig-stack-verify
wanconfig-stack-verify: $(WANCONFIG_STACK_TOOL)
	docker run --rm --platform linux/$(WANCONFIG_STACK_ARCH) \
		-v $(WANCONFIG_STACK_TOOL):/usr/local/bin/wanconfigstack:ro \
		-v $(WANCONFIG_STACK_CACHE):/out:ro \
		$(WANCONFIG_STACK_IMAGE) \
		wanconfigstack -verify /out/$(notdir $(WANCONFIG_STACK_CACHED))

# The release's compile stage for the linux artifact also produces the stack
# bundle, so one release tag delivers the binary and the packages it pairs
# with. The other stages and platforms never build it.
ifeq ($(RELEASE_STAGE),compile)
ifneq ($(filter linux/%,$(RELEASE_PLATFORMS)),)
release: | wanconfig-stack-bundle
endif
endif

# ---------------------------------------------------------------------------
# Maintenance
# ---------------------------------------------------------------------------

.PHONY: tidy govulncheck clean

tidy:
	go mod tidy

# mwan's wrapper filters one known gobgp vulnerability (GO-2026-4736) that
# upstream fixed in v4.3.0.
govulncheck:
	./tools/govulncheck.sh

clean: clean-dist
	rm -rf $(LOCAL_BIN)
