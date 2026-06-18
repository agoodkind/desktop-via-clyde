#!/usr/bin/env bash
set -euo pipefail

readonly LAUNCHD_LABEL="io.goodkind.desktop-via-clyde.updater"

usage() {
    printf 'usage: %s pre|post /path/to/desktop-via-clyde\n' "$0" >&2
}

state_dir() {
    printf '%s\n' "${DVC_INSTALL_GUARD_STATE_DIR:-.make}"
}

marker_path() {
    printf '%s/desktop-via-clyde-updater-was-loaded\n' "$(state_dir)"
}

launchd_target() {
    printf 'gui/%s/%s\n' "$(id -u)" "${LAUNCHD_LABEL}"
}

launchd_domain() {
    printf 'gui/%s\n' "$(id -u)"
}

plist_path() {
    printf '%s/Library/LaunchAgents/%s.plist\n' "${HOME}" "${LAUNCHD_LABEL}"
}

updater_is_loaded() {
    launchctl print "$(launchd_target)" >/dev/null 2>&1
}

stop_updater() {
    local install_bin
    install_bin="$1"

    if [[ "$(uname)" != "Darwin" ]]; then
        printf 'install_guard updater=skipped reason=non_darwin\n'
        return 0
    fi
    if ! command -v launchctl >/dev/null 2>&1; then
        printf 'install_guard updater=skipped reason=launchctl_missing\n'
        return 0
    fi
    rm -f "$(marker_path)"
    if ! updater_is_loaded; then
        printf 'install_guard updater=not_loaded\n'
        return 0
    fi

    mkdir -p "$(state_dir)"
    printf '%s\n' "${install_bin}" >"$(marker_path)"
    if [[ -x "${install_bin}" ]]; then
        if "${install_bin}" updater uninstall; then
            printf 'install_guard updater=stopped method=cli\n'
            return 0
        fi
    fi

    launchctl bootout "$(launchd_domain)" "$(plist_path)" >/dev/null 2>&1 || true
    rm -f "$(plist_path)"
    printf 'install_guard updater=stopped method=launchctl\n'
}

restore_updater() {
    local install_bin
    local marker
    install_bin="$1"
    marker="$(marker_path)"

    if [[ ! -f "${marker}" ]]; then
        printf 'install_guard updater=not_previously_loaded\n'
        return 0
    fi
    if [[ ! -x "${install_bin}" ]]; then
        printf 'install_guard updater=restore_failed reason=install_bin_missing path=%s\n' "${install_bin}" >&2
        return 1
    fi

    "${install_bin}" updater install
    rm -f "${marker}"
    printf 'install_guard updater=restarted\n'
}

main() {
    if [[ "$#" -ne 2 ]]; then
        usage
        return 2
    fi

    local action
    local install_bin
    action="$1"
    install_bin="$2"

    case "${action}" in
        pre)
            stop_updater "${install_bin}"
            ;;
        post)
            restore_updater "${install_bin}"
            ;;
        *)
            usage
            return 2
            ;;
    esac
}

main "$@"
