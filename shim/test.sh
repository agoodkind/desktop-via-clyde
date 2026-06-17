#!/usr/bin/env bash

set -euo pipefail

function has_swift_tests() {
    local tests_dir
    tests_dir="$1"

    [[ -d "${tests_dir}" ]] || return 1
    find "${tests_dir}" -type f -name '*.swift' -print -quit | grep -q .
}

function main() {
    local tests_dir
    tests_dir="Tests"

    if ! has_swift_tests "${tests_dir}"; then
        printf '%s\n' "shim/test.sh: no Swift tests found; skipping"
        return 0
    fi

    if [[ -n "${SWIFT_MK_SWIFTPM_CACHE_ARGS:-}" ]]; then
        # shellcheck disable=SC2086
        swift test ${SWIFT_MK_SWIFTPM_CACHE_ARGS}
        return 0
    fi

    swift test
}

main "$@"
