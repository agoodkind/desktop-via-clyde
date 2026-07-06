# desktop-via-clyde

`desktop-via-clyde` patches configured macOS desktop app bundles so normal
launches use Clyde-controlled local routing.

The tool modifies third-party app bundles in place and re-signs them with a
local identity. It is research software for machines and accounts the operator
controls. It is not affiliated with upstream app vendors. The software is
provided under the MIT License without warranty; see `LICENSE`.

## Current Truth

Runtime behavior is config-driven. Read the active target definitions from
`$XDG_CONFIG_HOME/desktop-via-clyde/config.toml`, or
`$HOME/.config/desktop-via-clyde/config.toml` when `XDG_CONFIG_HOME` is unset.

Runtime state lives under `$XDG_STATE_HOME/clyde`, or `$HOME/.local/state/clyde`
when `XDG_STATE_HOME` is unset. Patch state, logs, helper installs, generated
signing assets, and the Clyde MITM CA live under that state root.

Use the CLI help and status output for the current command surface:

```sh
desktop-via-clyde --help
desktop-via-clyde status
```

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/agoodkind/desktop-via-clyde/main/install.sh | bash
```

## Source Owners

`config.example.toml` documents the config shape. The schema lives in
`internal/spec`, and target materialization lives in `internal/config`,
`internal/extensions`, and `internal/targets`.

Patch orchestration lives in `internal/patch`. Development-profile signing and
external injector setup live in `internal/devsign`.

Upgrade and updater behavior live in `internal/upgrade` and `internal/daemon`.

The Swift launch shim lives in `shim`. The external injector source lives in
`injector`, with embedded build outputs under `internal/embed`.
