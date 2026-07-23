#!/usr/bin/env bash
set -euo pipefail

cleanup_paths=()

cleanup() {
    local path
    for path in "${cleanup_paths[@]}"; do
        rm -rf "${path}"
    done
}

trap cleanup EXIT

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${script_dir}/.." && pwd)"
injector_path="${repo_root}/internal/embed/clyde-inject.dylib"
host_c="${script_dir}/test_host.c"
tmp_dir="$(mktemp -d)"
cleanup_paths+=("${tmp_dir}")

policy_path="${tmp_dir}/policy.bin"
app_macos_dir="${tmp_dir}/FakeApp.app/Contents/MacOS"
app_host_bin="${app_macos_dir}/host"
plain_host_bin="${tmp_dir}/host"

printf 'set\0DVC_INJECT_TEST\0ok\0unset\0DVC_INJECT_REMOVE\0append-argv\0--dvc-inject-arg\0' > "${policy_path}"

mkdir -p "${app_macos_dir}"
xcrun clang -Wall -Wextra -Werror -o "${app_host_bin}" "${host_c}"
xcrun clang -Wall -Wextra -Werror -o "${plain_host_bin}" "${host_c}"

env \
    DYLD_INSERT_LIBRARIES="${injector_path}" \
    DVC_CLYDE_INJECT_POLICY="${policy_path}" \
    DVC_INJECT_REMOVE=present \
    "${app_host_bin}"

env \
    DYLD_INSERT_LIBRARIES="${injector_path}" \
    DVC_CLYDE_INJECT_POLICY="${policy_path}" \
    DVC_INJECT_REMOVE=present \
    DVC_INJECT_EXPECT_ARGV=0 \
    "${plain_host_bin}"

env \
    DYLD_INSERT_LIBRARIES="${injector_path}" \
    DVC_CLYDE_INJECT_POLICY="${policy_path}" \
    DVC_INJECT_REMOVE=present \
    ELECTRON_RUN_AS_NODE=1 \
    "${app_host_bin}" --dvc-inject-arg
