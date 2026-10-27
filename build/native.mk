# Native builds consume an explicitly selected SDK; SDK preparation is separate.
MWAN_NATIVE_DPDK_VERSION := 26.03.0
MWAN_NATIVE_DPDK_SHA256 := 84989202bbe0fbaeeb62f8fdc63f691880ac0b66cd0dc64c167843c59db29d2c
MWAN_NATIVE_PATCH_SHA256 := 18c92c1ab391fc8d7490f7a3e0641fa316340fa11464eaa4439c803d07c91df9
MWAN_NATIVE_ROOT := $(abspath $(CURDIR)/../native)
MWAN_NATIVE_PATCH := $(MWAN_NATIVE_ROOT)/patches/dpdk-26.03.patch
MWAN_NATIVE_BUILD := $(GO_MK_CGO_PREFIX)/mwan-native-build
MWAN_NATIVE_DPDK_PREFIX ?=
MWAN_NATIVE_SOURCE_FILES := $(wildcard $(MWAN_NATIVE_ROOT)/src/*.[ch]) $(wildcard $(MWAN_NATIVE_ROOT)/include/*.h) $(wildcard $(MWAN_NATIVE_ROOT)/*.in) $(MWAN_NATIVE_ROOT)/meson.build $(MWAN_NATIVE_ROOT)/meson_options.txt
MWAN_NATIVE_PKG_PATH := $(MWAN_NATIVE_DPDK_PREFIX)/lib/pkgconfig:$(MWAN_NATIVE_DPDK_PREFIX)/lib/$(shell $(CC) -dumpmachine)/pkgconfig

.PHONY: go-mk-cgo-dep-native native-sdk-required native-library native-hardware-driver
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
	PKG_CONFIG_PATH=$(MWAN_NATIVE_PKG_PATH) meson setup --clearcache --reconfigure $(MWAN_NATIVE_BUILD) $(MWAN_NATIVE_ROOT) --prefix=$(GO_MK_CGO_PREFIX) --libdir=lib -Ddpdk_source_sha256=$(MWAN_NATIVE_DPDK_SHA256) -Ddpdk_patch_sha256=$(MWAN_NATIVE_PATCH_SHA256)
	meson compile -C $(MWAN_NATIVE_BUILD)
	meson install -C $(MWAN_NATIVE_BUILD)

native-hardware-driver: native-library
	PKG_CONFIG_PATH=$(MWAN_NATIVE_PKG_PATH) meson configure $(MWAN_NATIVE_BUILD) -Dhardware_test_driver=true
	meson compile -C $(MWAN_NATIVE_BUILD)
