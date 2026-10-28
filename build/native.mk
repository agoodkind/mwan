# Native builds consume an explicitly selected SDK; SDK preparation is separate.
MWAN_NATIVE_DPDK_VERSION := 26.03.0
MWAN_NATIVE_DPDK_SHA256 := 84989202bbe0fbaeeb62f8fdc63f691880ac0b66cd0dc64c167843c59db29d2c
MWAN_NATIVE_PATCH_SHA256 := 18c92c1ab391fc8d7490f7a3e0641fa316340fa11464eaa4439c803d07c91df9
MWAN_NATIVE_ROOT := $(abspath $(CURDIR)/../native)
MWAN_NATIVE_PATCH := $(MWAN_NATIVE_ROOT)/patches/dpdk-26.03.patch
MWAN_NATIVE_BUILD := $(GO_MK_CGO_PREFIX)/mwan-native-build
MWAN_NATIVE_DPDK_PREFIX ?=
MWAN_NATIVE_RUST_TOOLCHAIN := $(shell sed -n 's/^channel = "\([^"]*\)"$$/\1/p' $(MWAN_NATIVE_ROOT)/../rust-toolchain.toml)
MWAN_NATIVE_RUST_TARGET ?= $(shell RUSTUP_TOOLCHAIN="$(MWAN_NATIVE_RUST_TOOLCHAIN)" rustc --print host-tuple)
MWAN_NATIVE_TEST_TOOLCHAIN := nightly-2026-10-27
MWAN_NATIVE_CARGO_FUZZ_VERSION := 0.13.2
MWAN_NATIVE_CARGO_TARGET := $(GO_MK_CGO_PREFIX)/mwan-native-cargo
MWAN_NATIVE_RUST_ARCHIVE = $(MWAN_NATIVE_CARGO_TARGET)/$(MWAN_NATIVE_RUST_TARGET)/release/libmwan_native.a
MWAN_NATIVE_RUST_LINK_FLAGS := $(GO_MK_CGO_PREFIX)/rust-native-link-flags.txt
MWAN_NATIVE_SOURCE_FILES := $(wildcard $(MWAN_NATIVE_ROOT)/src/*.rs) $(wildcard $(MWAN_NATIVE_ROOT)/src/controller/*.rs) $(MWAN_NATIVE_ROOT)/src/dpdk_shim.c $(MWAN_NATIVE_ROOT)/src/dpdk_shim.h $(wildcard $(MWAN_NATIVE_ROOT)/include/*.h) $(wildcard $(MWAN_NATIVE_ROOT)/*.in) $(MWAN_NATIVE_ROOT)/Cargo.toml $(MWAN_NATIVE_ROOT)/Cargo.lock $(MWAN_NATIVE_ROOT)/build.rs $(MWAN_NATIVE_ROOT)/../rust-toolchain.toml $(MWAN_NATIVE_ROOT)/meson.build $(MWAN_NATIVE_ROOT)/meson_options.txt
MWAN_NATIVE_PKG_PATH := $(MWAN_NATIVE_DPDK_PREFIX)/lib/pkgconfig:$(MWAN_NATIVE_DPDK_PREFIX)/lib/$(shell $(CC) -dumpmachine)/pkgconfig
MWAN_NATIVE_CARGO_ENV = PKG_CONFIG_PATH="$(MWAN_NATIVE_PKG_PATH)" MWAN_DPDK_SOURCE_SHA256="$(MWAN_NATIVE_DPDK_SHA256)" MWAN_DPDK_PATCH_SHA256="$(MWAN_NATIVE_PATCH_SHA256)" CARGO_TARGET_DIR="$(MWAN_NATIVE_CARGO_TARGET)" RUSTUP_TOOLCHAIN="$(MWAN_NATIVE_RUST_TOOLCHAIN)"
MWAN_NATIVE_FUZZ_RUNS ?= 100000

.PHONY: go-mk-cgo-dep-native native-sdk-required native-library native-hardware-driver native-rust-check native-rust-sdk-check native-rust-test-toolchain native-rust-miri native-rust-fuzz native-sdk-check native-admission-kernel-check native-admission-startup-check
go-mk-cgo-dep-native: native-library

native-sdk-required:
ifeq ($(strip $(MWAN_NATIVE_DPDK_PREFIX)),)
	@echo "Set MWAN_NATIVE_DPDK_PREFIX to an installed DPDK 26.03 SDK with the patches listed in native/patches/README.md." >&2
	@exit 1
else
	test -r $(MWAN_NATIVE_DPDK_PREFIX)/lib/pkgconfig/libdpdk.pc
	PKG_CONFIG_PATH=$(MWAN_NATIVE_PKG_PATH) pkg-config --exists 'libdpdk = $(MWAN_NATIVE_DPDK_VERSION)'
endif

native-library: native-sdk-required
	$(if $(strip $(MWAN_NATIVE_RUST_TOOLCHAIN)),,$(error Restore the Rust toolchain pin in rust-toolchain.toml))
	$(if $(strip $(MWAN_NATIVE_RUST_TARGET)),,$(error Install the pinned Rust toolchain or set MWAN_NATIVE_RUST_TARGET to its actual Linux target triple))
	mkdir -p "$(MWAN_NATIVE_BUILD)"
	$(MWAN_NATIVE_CARGO_ENV) MWAN_NATIVE_LINK_FLAGS_FILE="$(MWAN_NATIVE_RUST_LINK_FLAGS)" CARGO_ENCODED_RUSTFLAGS='-Ctarget-feature=+crt-static' cargo build --locked --manifest-path "$(MWAN_NATIVE_ROOT)/Cargo.toml" --target "$(MWAN_NATIVE_RUST_TARGET)" --release --features dpdk
	test -s "$(MWAN_NATIVE_RUST_ARCHIVE)"
	test -s "$(MWAN_NATIVE_RUST_LINK_FLAGS)"
	PKG_CONFIG_PATH=$(MWAN_NATIVE_PKG_PATH) meson setup --clearcache --reconfigure $(MWAN_NATIVE_BUILD) $(MWAN_NATIVE_ROOT) --prefix=$(GO_MK_CGO_PREFIX) --libdir=lib -Ddpdk_source_sha256=$(MWAN_NATIVE_DPDK_SHA256) -Ddpdk_patch_sha256=$(MWAN_NATIVE_PATCH_SHA256) -Dnative_archive="$(MWAN_NATIVE_RUST_ARCHIVE)" -Dnative_link_flags_file="$(MWAN_NATIVE_RUST_LINK_FLAGS)"
	PKG_CONFIG_PATH=$(MWAN_NATIVE_PKG_PATH) meson compile -C $(MWAN_NATIVE_BUILD)
	PKG_CONFIG_PATH=$(MWAN_NATIVE_PKG_PATH) meson install -C $(MWAN_NATIVE_BUILD)

native-rust-check:
	cargo fmt --manifest-path "$(MWAN_NATIVE_ROOT)/Cargo.toml" --check
	cargo clippy --locked --manifest-path "$(MWAN_NATIVE_ROOT)/Cargo.toml" --no-default-features --all-targets -- -D warnings
	cargo test --locked --manifest-path "$(MWAN_NATIVE_ROOT)/Cargo.toml" --no-default-features

native-rust-sdk-check: native-sdk-required
	$(MWAN_NATIVE_CARGO_ENV) cargo clippy --locked --manifest-path "$(MWAN_NATIVE_ROOT)/Cargo.toml" --features dpdk --all-targets -- -D warnings
	$(MWAN_NATIVE_CARGO_ENV) cargo test --locked --manifest-path "$(MWAN_NATIVE_ROOT)/Cargo.toml" --features dpdk

native-rust-test-toolchain:
	rustup toolchain install $(MWAN_NATIVE_TEST_TOOLCHAIN) --profile minimal --component miri,rust-src
	cargo install cargo-fuzz --version $(MWAN_NATIVE_CARGO_FUZZ_VERSION) --locked

native-rust-miri:
	cargo +$(MWAN_NATIVE_TEST_TOOLCHAIN) miri test --locked --manifest-path "$(MWAN_NATIVE_ROOT)/Cargo.toml" --no-default-features

native-rust-fuzz:
	cargo +$(MWAN_NATIVE_TEST_TOOLCHAIN) fuzz run --fuzz-dir "$(MWAN_NATIVE_ROOT)/fuzz" packet -- -runs=$(MWAN_NATIVE_FUZZ_RUNS) -max_len=65597

native-sdk-check: native-sdk-required
	$(MAKE) native-rust-sdk-check
	$(MAKE) native-hardware-driver
	$(MAKE) MWAN_NATIVE=1 build test

native-admission-kernel-check: native-library go-mk-cgo-dep-libyang go-mk-cgo-dep-sysrepo
	PKG_CONFIG_PATH="$(GO_MK_CGO_PREFIX)/lib/pkgconfig:$(MWAN_NATIVE_PKG_PATH)" go test -tags 'nativeoffload netns' ./internal/offload ./internal/firewall ./internal/ifmgr/modules/nativeoffload ./internal/ifmgr/modules/steering ./internal/ifmgr/modules/npt ./internal/ifmgr/modules/npt/bpf ./internal/netif -run 'Test(Conntrack|Forwarding|NativeSingleDirection|NativePacketObservation|NativeAdmission|NativeVXLAN|NativePacketPolicy|AppliedNPTNativePacketPolicy|Owned(Tunnel|VXLAN)|InspectNative|RulesetEvents|SteeringMap)' -count=1 -v

native-admission-startup-check: native-library go-mk-cgo-dep-libyang go-mk-cgo-dep-sysrepo
	PKG_CONFIG_PATH="$(GO_MK_CGO_PREFIX)/lib/pkgconfig:$(MWAN_NATIVE_PKG_PATH)" GOFLAGS="$(GOFLAGS) -tags=nativeoffload" go test -tags 'nativeoffload netns firewallnetns' ./cmd/mwan -run '^TestNativeStartup' -count=1 -v

native-hardware-driver: native-library
	PKG_CONFIG_PATH=$(MWAN_NATIVE_PKG_PATH) meson configure $(MWAN_NATIVE_BUILD) -Dhardware_test_driver=true
	PKG_CONFIG_PATH=$(MWAN_NATIVE_PKG_PATH) meson compile -C $(MWAN_NATIVE_BUILD)
