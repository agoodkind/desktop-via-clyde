SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

REPO_ROOT := $(shell pwd)
BIN_DIR := $(REPO_ROOT)/bin
INSTALL_DIR := $(HOME)/.local/bin
BINARY := $(BIN_DIR)/desktop-via-clyde
SHIM_OUT := $(REPO_ROOT)/internal/embed/shim

.DEFAULT_GOAL := build

.PHONY: shim build install check fmt clean

shim:
	$(REPO_ROOT)/shim/build.sh

# The Go binary embeds the shim, so the shim must exist before `go build`.
build: shim
	mkdir -p $(BIN_DIR)
	go build -o $(BINARY) ./cmd/desktop-via-clyde

install: build
	mkdir -p $(INSTALL_DIR)
	install -m 0755 $(BINARY) $(INSTALL_DIR)/desktop-via-clyde
	@echo "installed $(INSTALL_DIR)/desktop-via-clyde"

check: shim
	@echo "== gofmt =="
	@if [[ -n "$$(gofmt -l . 2>/dev/null | grep -v '^vendor/')" ]]; then \
		gofmt -l . | grep -v '^vendor/' ; \
		echo "gofmt: above files need formatting" >&2 ; \
		exit 1 ; \
	fi
	@echo "== go vet =="
	go vet ./...
	@echo "== go test =="
	go test ./...

fmt:
	gofmt -w .

clean:
	rm -rf $(BIN_DIR)
	rm -f $(SHIM_OUT)
