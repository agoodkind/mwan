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

# Each WANCONFIG_*_COMMIT records the full hash its version tag resolved to at
# review time. The packaging build fails if a tag resolves to another commit.
# Force-moving an upstream tag cannot change the packaged source.
# Update both the version and commit lines for a version bump.
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

# Only the static libyang recipe uses the PCRE2 pin. WANCONFIG_PINS excludes
# PCRE2 because the stack bundle does not build PCRE2 and the bundle cache
# key hashes WANCONFIG_PINS.
WANCONFIG_PCRE2_VERSION := pcre2-10.45
WANCONFIG_PCRE2_COMMIT  := 2dce7761b1831fd3f82a9c2bd5476259d945da4d
