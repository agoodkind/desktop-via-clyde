# `make help` is the canonical source of truth for the shared Swift targets.
# Consumer repos keep this file small and keep shared lint, build, audit,
# baseline, and update behavior in swift-makefile.

SWIFT_MK_MODULES := swift-build.mk
SWIFT_BUILD_CMD ?= ./build.sh
SWIFT_TEST_CMD ?= ./test.sh
SWIFT_RUN_CMD ?= swift run
SWIFT_CLEAN_CMD ?= ./clean.sh
SWIFT_FORMAT_TARGETS ?= Package.swift Sources
SWIFTLINT_TARGETS ?= Package.swift Sources
SWIFTCHECK_EXTRA_TARGETS ?= Package.swift Sources
SWIFT_MK_NOTICES_FILE ?= /dev/null
SWIFTLINT_BASELINE ?= .swiftlint-baseline.txt
SWIFTLINT_COMPLEXITY_BASELINE ?= .swiftlint-complexity-baseline.txt
PERIPHERY_BASELINE ?= .periphery-baseline.txt
SWIFTCHECK_EXTRA_BASELINE ?= .swiftcheck-extra-baseline.txt

include bootstrap.mk
override SWIFT_MK_NOTICES_FILE := /dev/null

.DEFAULT_GOAL := check
