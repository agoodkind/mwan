################################################################################
# Stack versions
################################################################################

# The cgo build uses the libyang and sysrepo pins. Stack packaging uses all
# six component pins instead of distribution versions.
# nghttp2-asio uses a commit because upstream does not publish release tags.
#
# libyang-cpp v4 requires the libyang 3.x API. libyang 5.x renamed print and
# parse symbols, and sysrepo v3.7.11 requires libyang 3.13.x. The C++ bindings'
# minimum-version constraints do not establish compatibility with newer majors.
#
# Deployment requires the binary's reported libsysrepo version to match
# wanconfig_sysrepo_soversion in the inventory. Update that value before
# deploying a release with a different sysrepo version.
WANCONFIG_LIBYANG_VERSION      := v3.13.6
WANCONFIG_SYSREPO_VERSION      := v3.7.11
WANCONFIG_LIBYANG_CPP_VERSION  := v4
WANCONFIG_SYSREPO_CPP_VERSION  := v6
WANCONFIG_NGHTTP2_ASIO_VERSION := e877868abe
WANCONFIG_ROUSETTE_VERSION     := v2

################################################################################
# Source commits
################################################################################

# Packaging verifies each version against its pinned commit and rejects
# upstream tag changes. Update the version and commit together.
WANCONFIG_LIBYANG_COMMIT      := c2ddd01b9b810a30d6a7d6749a3bc9adeb7b01fb
WANCONFIG_SYSREPO_COMMIT      := 1b720b196f630f348d9e0c131d326b3fb8c6aca7
WANCONFIG_LIBYANG_CPP_COMMIT  := 249da7280864fbda5fccb340b455b7000ebfe67d
WANCONFIG_SYSREPO_CPP_COMMIT  := 01bed8d91bfb746c20cc53ae4e8d64e2c78d2a9e
WANCONFIG_NGHTTP2_ASIO_COMMIT := e877868abe06a83ed0a6ac6e245c07f6f20866b5
WANCONFIG_ROUSETTE_COMMIT     := 4685b3379b259bc1b4aca79c92ca47d2b1e48e5e

################################################################################
# Stack cache inputs
################################################################################

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

################################################################################
# Native PCRE2
################################################################################

# Only the static libyang build uses PCRE2. Exclude it from WANCONFIG_PINS,
# which determines the stack bundle cache key.
WANCONFIG_PCRE2_VERSION := pcre2-10.45
WANCONFIG_PCRE2_COMMIT  := 2dce7761b1831fd3f82a9c2bd5476259d945da4d
