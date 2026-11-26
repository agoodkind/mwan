include ../build/wanconfig.env

################################################################################
# Library build settings
################################################################################

PCRE2_SOURCE  := $(GO_MK_CGO_PREFIX).src/pcre2
PCRE2_REPO    := https://github.com/PCRE2Project/pcre2.git
PCRE2_OPTIONS := -DPCRE2_BUILD_PCRE2GREP=OFF -DPCRE2_BUILD_TESTS=OFF

LIBYANG_SOURCE := $(GO_MK_CGO_PREFIX).src/libyang
LIBYANG_REPO   := https://github.com/CESNET/libyang.git
# Disable XXHash discovery to avoid linking Homebrew's dynamic library.
LIBYANG_OPTIONS := -DENABLE_TOOLS=OFF -DCMAKE_DISABLE_FIND_PACKAGE_XXHash=ON

YANG_CMAKE_OPTIONS := \
	-DCMAKE_BUILD_TYPE=Release \
	-DCMAKE_INSTALL_LIBDIR=lib \
	-DCMAKE_POSITION_INDEPENDENT_CODE=ON \
	-DBUILD_SHARED_LIBS=OFF

# cgo reads Libs rather than Libs.private when linking the provider.
LIBYANG_PC      := $(GO_MK_CGO_PREFIX)/lib/pkgconfig/libyang.pc
LIBYANG_PC_LIBS := Libs: -L$${libdir} -lyang -lpcre2-8 -lm

################################################################################
# Build cache
################################################################################

YANG_BUILD_SETTINGS := $(YANG_CMAKE_OPTIONS) $(PCRE2_OPTIONS) \
	$(LIBYANG_OPTIONS) $(LIBYANG_PC_LIBS)
YANG_SETTINGS_CHECKSUM := $(firstword $(shell printf '%s' '$(YANG_BUILD_SETTINGS)' | cksum))

# A change to either library version or the build settings requires a new stamp.
LIBYANG_BUILD_ID  := $(WANCONFIG_LIBYANG_COMMIT)-f$(YANG_SETTINGS_CHECKSUM)
YANG_STAMP_PREFIX := $(GO_MK_CGO_PREFIX)/.yang-native-
YANG_STAMP        := $(YANG_STAMP_PREFIX)$(WANCONFIG_PCRE2_COMMIT)-$(LIBYANG_BUILD_ID).stamp

GO_MK_CGO_CACHE_VERSIONS := pcre2=$(WANCONFIG_PCRE2_COMMIT) libyang=$(LIBYANG_BUILD_ID)
GO_MK_CGO_CACHE_INPUTS   := $(abspath ../build/yang.mk ../build/wanconfig.env)

################################################################################
# Install native libraries
################################################################################

.PHONY: go-mk-cgo-dep-yang
go-mk-cgo-dep-yang: $(YANG_STAMP)

$(YANG_STAMP):
	rm -f $(YANG_STAMP_PREFIX)*.stamp

	# libyang needs PCRE2 during its build.
	$(call yang_checkout,$(PCRE2_SOURCE),$(PCRE2_REPO),$(WANCONFIG_PCRE2_COMMIT))
	$(call yang_build,$(PCRE2_SOURCE),$(PCRE2_OPTIONS))
	$(call yang_checkout,$(LIBYANG_SOURCE),$(LIBYANG_REPO),$(WANCONFIG_LIBYANG_COMMIT))
	$(call yang_build,$(LIBYANG_SOURCE),$(LIBYANG_OPTIONS))

	sed 's|^Libs:.*|$(LIBYANG_PC_LIBS)|' $(LIBYANG_PC) > $(LIBYANG_PC).tmp
	mv $(LIBYANG_PC).tmp $(LIBYANG_PC)
	grep -qxF '$(LIBYANG_PC_LIBS)' $(LIBYANG_PC)
	test -f $(GO_MK_CGO_PREFIX)/lib/libpcre2-8.a
	test -f $(GO_MK_CGO_PREFIX)/lib/libyang.a

	rm -rf $(PCRE2_SOURCE) $(LIBYANG_SOURCE)
	: > $@

################################################################################
# Source checkout and compilation
################################################################################

define yang_checkout
	rm -rf $(1)
	git init --quiet $(1)
	git -C $(1) fetch --quiet --depth 1 $(2) $(3)
	git -C $(1) checkout --quiet --detach FETCH_HEAD
	test "$$(git -C $(1) rev-parse HEAD)" = "$(3)"
endef

define yang_build
	cmake -S $(1) -B $(1)/build \
		-DCMAKE_INSTALL_PREFIX=$(GO_MK_CGO_PREFIX) \
		-DCMAKE_PREFIX_PATH=$(GO_MK_CGO_PREFIX) \
		$(YANG_CMAKE_OPTIONS) $(2)
	cmake --build $(1)/build --parallel
	cmake --install $(1)/build
endef
