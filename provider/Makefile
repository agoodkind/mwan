BINARY     := terraform-provider-mwan
CMD        := .
GKLOG_VPKG := goodkind.io/gklog/version

GO_BUILD_EXTRA_FLAGS := -trimpath -buildvcs=false
RELEASE_PLATFORMS    ?= darwin/arm64 linux/amd64 linux/arm64
GO_MK_MODULES        := go-build.mk go-release.mk
GO_MK_WORKSPACE_USE  := . ../gateway
GO_MK_CGO_DEPS       := yang

include bootstrap.mk
include ../build/yang.mk

.DEFAULT_GOAL := help
