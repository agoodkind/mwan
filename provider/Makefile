BINARY     := terraform-provider-mwan
CMD        := .
GKLOG_VPKG := goodkind.io/gklog/version

GO_BUILD_EXTRA_FLAGS := -trimpath -buildvcs=false
RELEASE_PLATFORMS    ?= darwin/arm64 linux/amd64 linux/arm64
RELEASE_BINS         := $(BINARY):$(CMD):platforms=darwin/arm64,platforms=linux/amd64,platforms=linux/arm64

GO_MK_MODULES       := go-build.mk go-release.mk
GO_MK_WORKSPACE_USE := . ../gateway
GO_MK_CGO_DEPS      := yang

include bootstrap.mk
include ../build/yang.mk

GO_MK_CGO_CACHE_VERSIONS := $(YANG_NATIVE_CACHE_VERSIONS)
GO_MK_CGO_CACHE_INPUTS   := $(YANG_NATIVE_CACHE_INPUTS)

.DEFAULT_GOAL := help
