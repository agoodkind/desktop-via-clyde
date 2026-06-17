#!/usr/bin/env bash

set -euo pipefail

function main() {
    swift package clean

    if [[ -d ".build" ]]; then
        rm -rf .build
    fi
}

main "$@"
