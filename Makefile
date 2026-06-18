# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# desktop-via-clyde Makefile.
# Project-local generated prerequisites live under internal/embed.

# Identity.
BINARY     := desktop-via-clyde
CMD        := ./cmd/$(BINARY)
VPKG       := goodkind.io/desktop-via-clyde/internal/version
GKLOG_VPKG := goodkind.io/gklog/version
DIST_DIR   := bin
BUNDLE_ID  := io.goodkind.desktop-via-clyde

GO_BUILD_TAGS        := gklog_stamped
GO_BUILD_EXTRA_FLAGS := -trimpath

# Strict lint policy.
# This repo does not commit lint baselines. The shared go-makefile gates read
# empty device baselines so every current finding must be fixed in code.
override BASELINE :=
override GOLANGCI_LINT_BASELINE := /dev/null
override GOCYCLO_BASELINE := /dev/null
override DEADCODE_BASELINE := /dev/null
override STATICCHECK_EXTRA_BASELINE := /dev/null
override GO_MK_NOTICES_FILE := /dev/null
override GO_MK_APPLIED_NOTICES := .make/.go-mk-applied-notices

# Pipeline modules.
GO_MK_MODULES := go-build.mk
GO_MK_DEV_DIR ?= $(HOME)/Sites/go-makefile

# Machine-local signing and release overrides live in untracked config.mk.
-include config.mk

include bootstrap.mk

ifeq ($(strip $(CODESIGN_IDENTITY)),)
CODESIGN_IDENTITY := -
endif

.DEFAULT_GOAL := check

REPO_ROOT            := $(CURDIR)
SHIM_OUT             := $(REPO_ROOT)/internal/embed/shim
INJECTOR_OUT         := $(REPO_ROOT)/internal/embed/clyde-inject.dylib
INSTALL_GUARD        := $(REPO_ROOT)/scripts/install-updater-guard.sh

GO_MK_INSTALL_PRE_CMD = "$(INSTALL_GUARD)" pre "$(INSTALL_BIN)"
GO_MK_INSTALL_POST_CMD = "$(INSTALL_GUARD)" post "$(INSTALL_BIN)"

.PHONY: dvc-help-extras go-generated-prereqs shim shim-build shim-test shim-fmt shim-clean injector injector-build injector-test injector-clean clean-generated proto

go-generated-prereqs: proto shim-build injector-build

# Protobuf / gRPC codegen. Sources live under api/**/*.proto; config is
# buf.yaml + buf.gen.yaml with local go-tool plugins, so only the buf binary is
# needed and no network call is made. Wired as a build prerequisite so the
# generated Go stays in sync with the proto.
proto: ## Regenerate protobuf/gRPC Go code from api/**/*.proto via buf
	@command -v buf >/dev/null 2>&1 || go install github.com/bufbuild/buf/cmd/buf@v1.70.0
	@buf generate

shim:
	@$(MAKE) shim-build

shim-build:
	$(MAKE) -C $(REPO_ROOT)/shim build

shim-test:
	$(MAKE) -C $(REPO_ROOT)/shim test

shim-fmt:
	$(MAKE) -C $(REPO_ROOT)/shim fmt

shim-clean:
	$(MAKE) -C $(REPO_ROOT)/shim clean

injector:
	@$(MAKE) injector-build

injector-build:
	$(MAKE) -C $(REPO_ROOT)/injector build

injector-test:
	$(MAKE) -C $(REPO_ROOT)/injector test

injector-clean:
	$(MAKE) -C $(REPO_ROOT)/injector clean

# Package loading, vet, test, and the shared analyzers need the embedded Swift
# shim and injector present because go:embed validates files during load.
build build-check check lint lint-golangci lint-files lint-diff staticcheck-extra vet test govulncheck install deploy: go-generated-prereqs

ifneq ($(filter go-release.mk,$(GO_MK_MODULES)),)
release: go-generated-prereqs
endif

help: dvc-help-extras

dvc-help-extras:
	@printf '%s\n' 'Root-only entry points:'
	@printf '  %-40s %s\n' 'install' 'build and install the signed desktop-via-clyde binary'
	@printf '  %-40s %s\n' 'deploy' 'alias for install'
	@printf '\n'

test: shim-test injector-test

fmt: shim-fmt

clean-generated:
	rm -f $(SHIM_OUT) $(INJECTOR_OUT)

clean: clean-dist shim-clean injector-clean clean-generated
