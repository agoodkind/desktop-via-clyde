# desktop-via-clyde

`desktop-via-clyde` patches configured macOS Electron app bundles so normal app launches run through Clyde-controlled local routing.

This project modifies third-party app bundles in place and re-signs them with a local signing identity. It is research software for machines and accounts the operator controls. It is not affiliated with, endorsed by, or supported by any upstream app vendor. The software is provided under the MIT License without warranty; see `LICENSE`.

## Runtime Locations

The runtime config lives at `$XDG_CONFIG_HOME/desktop-via-clyde/config.toml`, with `$HOME/.config/desktop-via-clyde/config.toml` as the default path.

Runtime state lives under `$XDG_STATE_HOME/clyde`, with `$HOME/.local/state/clyde` as the default root. Patch state, app backups, helper installs, and the Clyde MITM CA all live under that state root.

The checked-in config fixture lives under `internal/testconfig/testdata`.

## Patched Bundle Shape

The patched app executable is the Swift launch shim at `<App>.app/Contents/MacOS/<ExecName>`.

The original vendor executable is kept at `<App>.app/Contents/MacOS/<ExecName>.real`.

The launch policy is serialized into `<App>.app/Contents/Resources/<ExecName>.launch-policy.json`.

Codex Desktop resolves its bundled CLI through `<Codex.app>/Contents/Resources/codex`.

## Source Locations

The CLI and linked extension composition live under `cmd/desktop-via-clyde` and `internal/composition`.

Config loading and target materialization live under `internal/config`, `internal/extensions`, and `internal/targets`.

Patch orchestration and launch-policy serialization live under `internal/patch`.

The Swift launch shim lives under `shim`, and its embedded payload lives under `internal/embed`.
