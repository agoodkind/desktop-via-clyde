# Lint is centralized in go-makefile. Do NOT define project-local lint,
# deadcode, audit, fmt, vet, or staticcheck targets here. They duplicate
# the central pipeline and let agents bypass strict rules. Run `make help`
# for the canonical entry points (build/check/lint/fmt) and per-linter
# sub-targets (lint-golangci, lint-format, lint-gocyclo, lint-deadcode,
# staticcheck-extra). Refresh baselines via the matching *-baseline target.
#
# desktop-via-clyde Makefile.
# Build and lint now run through go-makefile, while the two embedded shim
# binaries remain project-local prerequisites because the Go binary will not
# even load its embed package cleanly unless those artifacts already exist.

# Identity.
BINARY     := desktop-via-clyde
CMD        := ./cmd/$(BINARY)
VPKG       := goodkind.io/desktop-via-clyde/internal/version
GKLOG_VPKG := goodkind.io/gklog/version
DIST_DIR   := bin

INSTALL_DIR          := $(HOME)/.local/bin
CODESIGN_IDENTITY    := -
GO_BUILD_EXTRA_FLAGS := -trimpath

# Pipeline modules.
GO_MK_MODULES := go-build.mk

include bootstrap.mk

.DEFAULT_GOAL := check

REPO_ROOT            := $(CURDIR)
SHIM_OUT             := $(REPO_ROOT)/internal/embed/shim
STDIO_TEE_SHIM_OUT   := $(REPO_ROOT)/internal/embed/dvc-stdio-tee-shim
STDIO_TEE_SHIM_BUILD := $(REPO_ROOT)/bin/dvc-stdio-tee-shim-build

.PHONY: shim stdio-tee-shim generated-shims clean-generated

generated-shims: shim stdio-tee-shim

# Package loading, vet, test, and the shared analyzers all need the embedded
# binaries present first because go:embed validates the files during load.
build build-check check lint lint-golangci lint-files lint-diff staticcheck-extra vet test govulncheck: generated-shims

shim:
	$(REPO_ROOT)/shim/build.sh

# stdio-tee-shim is a Go program. It builds a universal Mach-O via two arch
# passes plus lipo so the embedded binary runs on both arm64 and x86_64 Macs.
# The output lands in internal/embed/ so go:embed picks it up at main-binary
# build time.
stdio-tee-shim:
	mkdir -p $(STDIO_TEE_SHIM_BUILD)
	GOOS=darwin GOARCH=arm64 go build -trimpath -o $(STDIO_TEE_SHIM_BUILD)/arm64 ./cmd/dvc-stdio-tee-shim
	GOOS=darwin GOARCH=amd64 go build -trimpath -o $(STDIO_TEE_SHIM_BUILD)/amd64 ./cmd/dvc-stdio-tee-shim
	/usr/bin/lipo -create -output $(STDIO_TEE_SHIM_OUT) $(STDIO_TEE_SHIM_BUILD)/arm64 $(STDIO_TEE_SHIM_BUILD)/amd64
	rm -rf $(STDIO_TEE_SHIM_BUILD)
	/usr/bin/codesign --force --sign - --options runtime $(STDIO_TEE_SHIM_OUT)
	/usr/bin/file $(STDIO_TEE_SHIM_OUT)

clean-generated:
	rm -rf $(STDIO_TEE_SHIM_BUILD)
	rm -f $(SHIM_OUT) $(STDIO_TEE_SHIM_OUT)

clean: clean-dist clean-generated
