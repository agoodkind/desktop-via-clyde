#!/usr/bin/env bash
# Thin installer. Routes to go-makefile's hosted installer, which fetches and
# verifies go-mk-install, then installs the desktop-via-clyde release binary.
set -euo pipefail
curl -fsSL https://raw.githubusercontent.com/agoodkind/go-makefile/main/install.sh \
    | bash -s -- --repo agoodkind/desktop-via-clyde --binary desktop-via-clyde "$@"
