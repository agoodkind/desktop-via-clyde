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
tmp_dir="$(mktemp -d)"
cleanup_paths+=("${tmp_dir}")

policy_path="${tmp_dir}/policy.bin"
host_c="${tmp_dir}/host.c"
host_bin="${tmp_dir}/host"

printf 'set\0DVC_INJECT_TEST\0ok\0unset\0DVC_INJECT_REMOVE\0append-argv\0--dvc-inject-arg\0' > "${policy_path}"

cat > "${host_c}" <<'HOST'
#include <crt_externs.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

int main(void) {
    const char *inserted = getenv("DYLD_INSERT_LIBRARIES");
    const char *set_value = getenv("DVC_INJECT_TEST");
    const char *removed_value = getenv("DVC_INJECT_REMOVE");
    int argc = *_NSGetArgc();
    char **argv = *_NSGetArgv();
    int found_arg = 0;

    if (inserted != NULL) {
        fprintf(stderr, "DYLD_INSERT_LIBRARIES still set\n");
        return 10;
    }
    if (set_value == NULL || strcmp(set_value, "ok") != 0) {
        fprintf(stderr, "DVC_INJECT_TEST missing\n");
        return 11;
    }
    if (removed_value != NULL) {
        fprintf(stderr, "DVC_INJECT_REMOVE still set\n");
        return 12;
    }
    for (int i = 0; i < argc; i++) {
        if (strcmp(argv[i], "--dvc-inject-arg") == 0) {
            found_arg = 1;
        }
    }
    if (!found_arg) {
        fprintf(stderr, "argv append missing\n");
        return 13;
    }
    return 0;
}
HOST

xcrun clang -Wall -Wextra -Werror -o "${host_bin}" "${host_c}"

env \
    DYLD_INSERT_LIBRARIES="${injector_path}" \
    DVC_CLYDE_INJECT_POLICY="${policy_path}" \
    DVC_INJECT_REMOVE=present \
    "${host_bin}"

env \
    DYLD_INSERT_LIBRARIES="${injector_path}" \
    DVC_CLYDE_INJECT_POLICY="${policy_path}" \
    DVC_INJECT_REMOVE=present \
    ELECTRON_RUN_AS_NODE=1 \
    "${host_bin}" --dvc-inject-arg
