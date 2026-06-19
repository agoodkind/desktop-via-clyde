#!/usr/bin/env bash
# Builds the desktop-via-clyde shim as a universal Mach-O (arm64 + x86_64), signs it
# with the identity swift-mk resolves (--options runtime), and emits it at
# ../internal/embed/shim.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
EMBED_OUT="${REPO_ROOT}/internal/embed/shim"

build_arch() {
    local arch="$1"
    local out_path="$2"
    local built_bin=""
    cd "${SCRIPT_DIR}"
    swift build \
        --configuration release \
        --triple "${arch}-apple-macosx12.0" \
        --disable-sandbox >&2
    if [[ -f "${SCRIPT_DIR}/.build/${arch}-apple-macosx/release/Shim" ]]; then
        built_bin="${SCRIPT_DIR}/.build/${arch}-apple-macosx/release/Shim"
    elif [[ -f "${SCRIPT_DIR}/.build/${arch}-apple-macosx12.0/release/Shim" ]]; then
        built_bin="${SCRIPT_DIR}/.build/${arch}-apple-macosx12.0/release/Shim"
    fi
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

    sign_embedded_binary "${EMBED_OUT}"

    echo "shim/build.sh: wrote ${EMBED_OUT}" >&2
    /usr/bin/file "${EMBED_OUT}" >&2
}

# Sign with the identity swift-mk resolves rather than a hardcoded ad-hoc `-`, so
# signing is swift-mk-owned. signing-identity returns a real identity when one is
# configured, or ad-hoc `-` only because this package ("Shim") is on swift-mk's
# compiled-in allowlist for embedded helpers the product re-signs. It runs from the
# package directory so swift-mk reads the package name; SWIFT_MK_BIN comes from the
# make layer, so this must build via `make build`.
sign_embedded_binary() {
    local out_path="$1"
    local swift_mk_bin="${SWIFT_MK_BIN:-}"
    if [[ -z "${swift_mk_bin}" || ! -x "${swift_mk_bin}" ]]; then
        echo "shim/build.sh: SWIFT_MK_BIN is unset; run via 'make build' so swift-mk owns signing" >&2
        return 1
    fi
    local identity
    identity="$(cd "${SCRIPT_DIR}" && "${swift_mk_bin}" signing-identity)"
    if [[ -z "${identity}" ]]; then
        echo "shim/build.sh: swift-mk resolved no signing identity for this package" >&2
        return 1
    fi
    /usr/bin/codesign --force --sign "${identity}" --options runtime "${out_path}"
}

main "$@"
