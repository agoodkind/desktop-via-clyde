#!/usr/bin/env bash
# Builds the desktop-via-clyde shim as a universal Mach-O (arm64 + x86_64),
# ad-hoc signs it with --options runtime, and emits it at
# ../internal/embed/shim.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
EMBED_OUT="${REPO_ROOT}/internal/embed/shim"

build_arch() {
    local arch="$1"
    local out_path="$2"
    cd "${SCRIPT_DIR}"
    swift build \
        --configuration release \
        --triple "${arch}-apple-macosx12.0" \
        --disable-sandbox >&2
    local built_bin
    built_bin="$(swift build --configuration release --triple "${arch}-apple-macosx12.0" --show-bin-path)/Shim"
    if [[ ! -f "${built_bin}" ]]; then
        echo "shim/build.sh: missing build output ${built_bin}" >&2
        return 1
    fi
    cp "${built_bin}" "${out_path}"
}

tmpdir=""

main() {
    mkdir -p "${REPO_ROOT}/internal/embed"
    tmpdir="$(mktemp -d -t dvc-shim-build.XXXXXX)"
    trap 'rm -rf "${tmpdir:-}"' EXIT

    local arm64_bin="${tmpdir}/Shim.arm64"
    local x86_64_bin="${tmpdir}/Shim.x86_64"

    build_arch "arm64" "${arm64_bin}"
    build_arch "x86_64" "${x86_64_bin}"

    /usr/bin/lipo -create -output "${EMBED_OUT}" "${arm64_bin}" "${x86_64_bin}"

    /usr/bin/codesign --force --sign - --options runtime "${EMBED_OUT}"

    echo "shim/build.sh: wrote ${EMBED_OUT}" >&2
    /usr/bin/file "${EMBED_OUT}" >&2
}

main "$@"
