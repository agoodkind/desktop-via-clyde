#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
out_path="${repo_root}/internal/embed/clyde-inject.dylib"
src_path="${script_dir}/clyde_inject.c"

mkdir -p "$(dirname "${out_path}")"

xcrun clang \
    -dynamiclib \
    -arch arm64 \
    -arch x86_64 \
    -mmacosx-version-min=12.0 \
    -Wall \
    -Wextra \
    -Werror \
    -o "${out_path}" \
    "${src_path}"
