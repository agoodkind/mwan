YANG_NATIVE_MK      := $(abspath $(lastword $(MAKEFILE_LIST)))
YANG_NATIVE_MK_DIR  := $(patsubst %/,%,$(dir $(YANG_NATIVE_MK)))
YANG_NATIVE_PINS_MK := $(YANG_NATIVE_MK_DIR)/wanconfig.mk

include $(YANG_NATIVE_PINS_MK)

YANG_NATIVE_HOST_OS   := $(shell uname -s | tr '[:upper:]' '[:lower:]')
YANG_NATIVE_HOST_ARCH := $(patsubst aarch64,arm64,$(patsubst x86_64,amd64,$(shell uname -m)))
YANG_NATIVE_PLATFORM  := $(YANG_NATIVE_HOST_OS)-$(YANG_NATIVE_HOST_ARCH)

# go.mk defines GO_MK_CGO_PREFIX only when GO_MK_CGO_DEPS is set and exports
# the cached prefix through PKG_CONFIG_PATH. The standalone default uses a
# per-platform directory under the including module's .make.
ifneq ($(strip $(GO_MK_CGO_DEPS)),)
YANG_NATIVE_PREFIX ?= $(GO_MK_CGO_PREFIX)
else
YANG_NATIVE_PREFIX ?= $(CURDIR)/.make/yang/$(YANG_NATIVE_PLATFORM)
endif

YANG_NATIVE_SRC ?= $(or $(XDG_CACHE_HOME),$(HOME)/.cache)/mwan/yang-native-src/$(patsubst /%,%,$(CURDIR))/$(YANG_NATIVE_PLATFORM)

YANG_NATIVE_PCRE2_URL   ?= https://github.com/PCRE2Project/pcre2.git
YANG_NATIVE_LIBYANG_URL ?= https://github.com/CESNET/libyang.git

YANG_NATIVE_COMMON_CMAKE_FLAGS := \
	-DCMAKE_BUILD_TYPE=Release \
	-DCMAKE_INSTALL_LIBDIR=lib \
	-DCMAKE_POSITION_INDEPENDENT_CODE=ON \
	-DBUILD_SHARED_LIBS=OFF

YANG_NATIVE_PCRE2_CMAKE_FLAGS := \
	-DBUILD_STATIC_LIBS=ON \
	-DPCRE2_BUILD_PCRE2_8=ON \
	-DPCRE2_BUILD_PCRE2_16=OFF \
	-DPCRE2_BUILD_PCRE2_32=OFF \
	-DPCRE2_SUPPORT_UNICODE=ON \
	-DPCRE2_SUPPORT_JIT=OFF \
	-DPCRE2_BUILD_PCRE2GREP=OFF \
	-DPCRE2_BUILD_TESTS=OFF

# -DCMAKE_DISABLE_FIND_PACKAGE_XXHash=ON prevents CMake on macOS from
# linking Homebrew's dynamic xxhash. ENABLE_TOOLS must be off because
# libyang's executables fail to link against the static archive.
YANG_NATIVE_LIBYANG_CMAKE_FLAGS := \
	-DENABLE_TOOLS=OFF \
	-DENABLE_TESTS=OFF \
	-DCMAKE_DISABLE_FIND_PACKAGE_XXHash=ON

# pkg-config prints only Libs because cgo omits --static. Static linking
# requires -lpcre2-8 -lm in Libs. The recipe fails if the rewritten
# libyang.pc Libs line differs from the required value.
YANG_NATIVE_LIBYANG_PC_LIBS := Libs: -L$${libdir} -lyang -lpcre2-8 -lm

YANG_NATIVE_FLAGS_REV := $(shell printf '%s' '$(YANG_NATIVE_COMMON_CMAKE_FLAGS) $(YANG_NATIVE_PCRE2_CMAKE_FLAGS) $(YANG_NATIVE_LIBYANG_CMAKE_FLAGS) $(YANG_NATIVE_LIBYANG_PC_LIBS)' | cksum | cut -d' ' -f1)

YANG_NATIVE_STAMP_PREFIX := $(YANG_NATIVE_PREFIX)/.yang-native-
YANG_NATIVE_STAMP        := $(YANG_NATIVE_STAMP_PREFIX)$(WANCONFIG_PCRE2_COMMIT)-$(WANCONFIG_LIBYANG_COMMIT)-f$(YANG_NATIVE_FLAGS_REV).stamp
YANG_NATIVE_LIBYANG_PC   := $(YANG_NATIVE_PREFIX)/lib/pkgconfig/libyang.pc

YANG_NATIVE_PKG_CONFIG_PATH := $(YANG_NATIVE_PREFIX)/lib/pkgconfig
YANG_NATIVE_CGO_CFLAGS      := -I$(YANG_NATIVE_PREFIX)/include
YANG_NATIVE_CGO_LDFLAGS     := -L$(YANG_NATIVE_PREFIX)/lib

YANG_NATIVE_CACHE_VERSIONS := \
	pcre2=$(WANCONFIG_PCRE2_COMMIT) \
	libyang=$(WANCONFIG_LIBYANG_COMMIT)-f$(YANG_NATIVE_FLAGS_REV)
YANG_NATIVE_CACHE_INPUTS := $(YANG_NATIVE_MK) $(YANG_NATIVE_PINS_MK)

define yang_native_checkout
	rm -rf $(YANG_NATIVE_SRC)/$(1)
	git init --quiet $(YANG_NATIVE_SRC)/$(1)
	git -C $(YANG_NATIVE_SRC)/$(1) fetch --quiet --depth 1 $(2) $(3)
	git -C $(YANG_NATIVE_SRC)/$(1) checkout --quiet --detach FETCH_HEAD
	test "$$(git -C $(YANG_NATIVE_SRC)/$(1) rev-parse HEAD)" = "$(3)"
endef

define yang_native_build
	cmake -S $(YANG_NATIVE_SRC)/$(1) -B $(YANG_NATIVE_SRC)/$(1)/build \
		-DCMAKE_INSTALL_PREFIX=$(YANG_NATIVE_PREFIX) \
		-DCMAKE_PREFIX_PATH=$(YANG_NATIVE_PREFIX) \
		$(YANG_NATIVE_COMMON_CMAKE_FLAGS) \
		$(2)
	cmake --build $(YANG_NATIVE_SRC)/$(1)/build --parallel
	cmake --install $(YANG_NATIVE_SRC)/$(1)/build
endef

.PHONY: yang-native-prefix go-mk-cgo-dep-yang
yang-native-prefix: $(YANG_NATIVE_STAMP)
go-mk-cgo-dep-yang: yang-native-prefix

$(YANG_NATIVE_STAMP):
	rm -f $(YANG_NATIVE_STAMP_PREFIX)*.stamp
	$(call yang_native_checkout,pcre2,$(YANG_NATIVE_PCRE2_URL),$(WANCONFIG_PCRE2_COMMIT))
	$(call yang_native_build,pcre2,$(YANG_NATIVE_PCRE2_CMAKE_FLAGS))
	$(call yang_native_checkout,libyang,$(YANG_NATIVE_LIBYANG_URL),$(WANCONFIG_LIBYANG_COMMIT))
	$(call yang_native_build,libyang,$(YANG_NATIVE_LIBYANG_CMAKE_FLAGS))
	awk -v libs='$(YANG_NATIVE_LIBYANG_PC_LIBS)' '/^Libs:/ {print libs; next} {print}' $(YANG_NATIVE_LIBYANG_PC) > $(YANG_NATIVE_LIBYANG_PC).tmp
	mv $(YANG_NATIVE_LIBYANG_PC).tmp $(YANG_NATIVE_LIBYANG_PC)
	grep -qxF '$(YANG_NATIVE_LIBYANG_PC_LIBS)' $(YANG_NATIVE_LIBYANG_PC)
	test -f $(YANG_NATIVE_PREFIX)/lib/libpcre2-8.a
	test -f $(YANG_NATIVE_PREFIX)/lib/libyang.a
	: > $@
