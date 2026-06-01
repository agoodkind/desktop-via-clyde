# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# desktop-via-clyde Makefile.
# The Swift launch shim is the only project-local generated prerequisite.

# Identity.
BINARY     := desktop-via-clyde
CMD        := ./cmd/$(BINARY)
VPKG       := goodkind.io/desktop-via-clyde/internal/version
GKLOG_VPKG := goodkind.io/gklog/version
DIST_DIR   := bin

INSTALL_DIR          := $(HOME)/.local/bin
CODESIGN_IDENTITY    := -
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

include bootstrap.mk

.DEFAULT_GOAL := check

REPO_ROOT            := $(CURDIR)
SHIM_OUT             := $(REPO_ROOT)/internal/embed/shim

.PHONY: shim generated-shims clean-generated

generated-shims: shim

# Package loading, vet, test, and the shared analyzers need the embedded Swift
# shim present because go:embed validates the file during load.
build build-check check lint lint-golangci lint-files lint-diff staticcheck-extra vet test govulncheck: generated-shims

shim:
	$(MAKE) -C $(REPO_ROOT)/shim SWIFT_MK_DEV_DIR=$(HOME)/Sites/swift-makefile SWIFT_MK_NOTICES_FILE=/dev/null LINT_GATES='lint-swiftlint lint-format lint-complexity lint-deadcode swiftcheck-extra' build

clean-generated:
	rm -f $(SHIM_OUT)

clean: clean-dist clean-generated
