################################################################################
# Settings
################################################################################

include ../build/wanconfig.env

YANG_NATIVE_SRC := $(GO_MK_CGO_PREFIX).src

################################################################################
# Build flags
################################################################################

YANG_NATIVE_COMMON_CMAKE_FLAGS := \
	-DCMAKE_BUILD_TYPE=Release \
	-DCMAKE_INSTALL_LIBDIR=lib \
	-DCMAKE_POSITION_INDEPENDENT_CODE=ON \
	-DBUILD_SHARED_LIBS=OFF

YANG_NATIVE_PCRE2_CMAKE_FLAGS := \
	-DPCRE2_BUILD_PCRE2GREP=OFF \
	-DPCRE2_BUILD_TESTS=OFF

# Disable XXHash discovery to avoid linking Homebrew's dynamic library.
YANG_NATIVE_LIBYANG_CMAKE_FLAGS := \
	-DENABLE_TOOLS=OFF \
	-DCMAKE_DISABLE_FIND_PACKAGE_XXHash=ON

# cgo invokes pkg-config without --static, so Libs must include libyang's dependencies.
YANG_NATIVE_LIBYANG_PC_LIBS := Libs: -L$${libdir} -lyang -lpcre2-8 -lm

################################################################################
# Cache settings
################################################################################

YANG_NATIVE_FLAGS_REV := $(shell printf '%s' '$(YANG_NATIVE_COMMON_CMAKE_FLAGS) $(YANG_NATIVE_PCRE2_CMAKE_FLAGS) $(YANG_NATIVE_LIBYANG_CMAKE_FLAGS) $(YANG_NATIVE_LIBYANG_PC_LIBS)' | cksum | cut -d' ' -f1)

YANG_NATIVE_STAMP_PREFIX := $(GO_MK_CGO_PREFIX)/.yang-native-
YANG_NATIVE_STAMP        := $(YANG_NATIVE_STAMP_PREFIX)$(WANCONFIG_PCRE2_COMMIT)-$(WANCONFIG_LIBYANG_COMMIT)-f$(YANG_NATIVE_FLAGS_REV).stamp
YANG_NATIVE_LIBYANG_PC   := $(GO_MK_CGO_PREFIX)/lib/pkgconfig/libyang.pc

GO_MK_CGO_CACHE_VERSIONS := \
	pcre2=$(WANCONFIG_PCRE2_COMMIT) \
	libyang=$(WANCONFIG_LIBYANG_COMMIT)-f$(YANG_NATIVE_FLAGS_REV)
GO_MK_CGO_CACHE_INPUTS := $(abspath ../build/yang.mk ../build/wanconfig.env)

################################################################################
# Build helpers
################################################################################

define yang_native_checkout
	rm -rf $(YANG_NATIVE_SRC)/$(1)
	git init --quiet $(YANG_NATIVE_SRC)/$(1)
	git -C $(YANG_NATIVE_SRC)/$(1) fetch --quiet --depth 1 $(2) $(3)
	git -C $(YANG_NATIVE_SRC)/$(1) checkout --quiet --detach FETCH_HEAD
	test "$$(git -C $(YANG_NATIVE_SRC)/$(1) rev-parse HEAD)" = "$(3)"
endef

define yang_native_build
	cmake -S $(YANG_NATIVE_SRC)/$(1) -B $(YANG_NATIVE_SRC)/$(1)/build \
		-DCMAKE_INSTALL_PREFIX=$(GO_MK_CGO_PREFIX) \
		-DCMAKE_PREFIX_PATH=$(GO_MK_CGO_PREFIX) \
		$(YANG_NATIVE_COMMON_CMAKE_FLAGS) \
		$(2)
	cmake --build $(YANG_NATIVE_SRC)/$(1)/build --parallel
	cmake --install $(YANG_NATIVE_SRC)/$(1)/build
endef

################################################################################
# Native libraries
################################################################################

.PHONY: go-mk-cgo-dep-yang
go-mk-cgo-dep-yang: $(YANG_NATIVE_STAMP)

$(YANG_NATIVE_STAMP):
	rm -f $(YANG_NATIVE_STAMP_PREFIX)*.stamp
	$(call yang_native_checkout,pcre2,https://github.com/PCRE2Project/pcre2.git,$(WANCONFIG_PCRE2_COMMIT))
	$(call yang_native_build,pcre2,$(YANG_NATIVE_PCRE2_CMAKE_FLAGS))
	$(call yang_native_checkout,libyang,https://github.com/CESNET/libyang.git,$(WANCONFIG_LIBYANG_COMMIT))
	$(call yang_native_build,libyang,$(YANG_NATIVE_LIBYANG_CMAKE_FLAGS))
	awk -v libs='$(YANG_NATIVE_LIBYANG_PC_LIBS)' '/^Libs:/ {print libs; next} {print}' $(YANG_NATIVE_LIBYANG_PC) > $(YANG_NATIVE_LIBYANG_PC).tmp
	mv $(YANG_NATIVE_LIBYANG_PC).tmp $(YANG_NATIVE_LIBYANG_PC)
	grep -qxF '$(YANG_NATIVE_LIBYANG_PC_LIBS)' $(YANG_NATIVE_LIBYANG_PC)
	test -f $(GO_MK_CGO_PREFIX)/lib/libpcre2-8.a
	test -f $(GO_MK_CGO_PREFIX)/lib/libyang.a
	rm -rf $(YANG_NATIVE_SRC)/pcre2 $(YANG_NATIVE_SRC)/libyang
	: > $@
