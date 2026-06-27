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

# Pipeline modules.
GO_MK_MODULES := go-build.mk
GO_MK_DEV_DIR ?= $(HOME)/Sites/go-makefile

# Codegen hook: go.mk runs go-generated-prereqs (proto plus the go:embed shim and
# injector payloads) as an order-only prerequisite of every build, lint, vet,
# test, and govulncheck target, so the embedded files exist before any target
# compiles a package that go:embeds them.
GO_MK_GENERATE := go-generated-prereqs
GO_MK_GENERATE_INPUTS := shim injector api buf.yaml buf.gen.yaml
GO_MK_GENERATE_OUTPUTS := internal/embed/shim internal/embed/clyde-inject.dylib shim/.make/osv-scanner.toml

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

# go.mk wires go-generated-prereqs (via GO_MK_GENERATE above) into every build,
# lint, vet, test, govulncheck, install, and release target. The targets below
# sit outside that central set but still load the go:embed package, so they keep
# the prerequisite here. lint-format is one of them: unlike a C-parser consumer,
# this repo's generated code is Go and embedded payloads, so the formatter's
# package load fails when go:embed targets are missing.
lint-format lint-files lint-diff: | go-generated-prereqs

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
