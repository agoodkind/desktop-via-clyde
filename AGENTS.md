# AGENTS.md

`desktop-via-clyde` patches macOS Electron app bundles in place and re-signs them with a local identity, so normal launches route through Clyde. It is research software for machines the operator controls. See `README.md` for scope and `LICENSE` for terms.

## Configuration

The CLI and the updater daemon read one file: `config.toml` under the XDG config root for `desktop-via-clyde` (the resolver is `internal/config/xdg.go`). Runtime state, logs, patch state, and the Clyde MITM CA sit under the XDG state root, in `clyde/`.

`config.example.toml` at the repo root documents the full shape: the `[signing]` identity, one `[apps.<id>]` target with its entitlements, updater, launch policy, and operations, and a standalone CLI block. The authoritative schema is the Go structs in `internal/spec/spec.go`, and `internal/testconfig/testdata` holds the schema-tested fixture.

Read every per-target value from the live config: app path, executable name, signing identity, proxy host and port, CA path, updater endpoint. Do not copy any of them into code, docs, or your reasoning. To see what is configurable, read `config.example.toml` rather than a list here.

## Patched bundle shape

- The launch shim is the app executable at `<App>.app/Contents/MacOS/<ExecName>`.
- The original vendor binary is moved to `<App>.app/Contents/MacOS/<ExecName>.real`.
- The launch policy is written to `<App>.app/Contents/Resources/<ExecName>.launch-policy.json`.

## Source layout

- CLI entry and composition: `cmd/desktop-via-clyde`, `internal/composition`.
- Config load and target materialization: `internal/config`, `internal/extensions`, `internal/targets`.
- Patch orchestration and launch-policy serialization: `internal/patch`.
- Nested-code discovery and signing: `internal/patch`, `internal/bundleidentity`, `internal/signing`.
- Upgrade and the updater daemon: `internal/upgrade`, `internal/daemon`.
- Swift launch shim: `shim`, with the embedded payload under `internal/embed`.

For conceptual lookups, lean on the semantic code search over the indexed repo before grepping. Check the index first, and fall back to ripgrep if results are low-signal.

## The signing rule that decides whether a patched app launches

The patcher re-signs the bundle with the local Developer ID under hardened runtime. Most nested code is re-signed to the local team. Some objects can be kept on their vendor signature through `preserved_nested_code_paths`.

The rule:

> Every nested Mach-O that a process loads must share the bundle's signing team, or the loading process must carry `com.apple.security.cs.disable-library-validation`.

Hardened-runtime library validation enforces this at launch. If you preserve a vendor framework on its original team while re-signing its loaders to the local team, any loader without that entitlement aborts at launch. Electron renderer helpers carry only `com.apple.security.cs.allow-jit`, so they have no exemption. The symptom is that the main process starts but every Electron helper crash-loops, so no window appears. The crash report says the loader and the mapped file "have different Team IDs."

A passing `codesign --verify` or `spctl` check does not rule this out, because library validation only happens at runtime. Confirm team consistency by reading the signatures across the bundle, and confirm a real launch by checking that helper processes stay alive and no new crash reports appear.

Treat `preserved_nested_code_paths` as a last resort. Preserve a framework only if re-signing it actually breaks something, and prove that by launching. Re-signing is the default and keeps the whole bundle on one team.

## Diagnosing a patched app that will not launch

- Start at the crash reports for the app and its helpers. The termination reasons are usually the exact cause.
- A "Library not loaded" or "different Team IDs" reason points at the signing rule above: find the off-team framework and re-sign it to the bundle team.
- Entitlement, keychain, and Secure Enclave failures have their own writeup in `docs/secure-enclave-keychain.md`.
- A passing signature check does not mean the app will launch.

## The updater daemon

The daemon checks the upstream manifest, swaps in new builds, and re-runs the patch. It reads the live config each time, so durable fixes must land in the live config, and in the repo defaults so a regenerated config does not drift back.

The CLI's `updater` verb is the durable interface: it can report state, install the daemon, and remove it, without you needing to know the LaunchAgent details. To pause it briefly while you work on a bundle, take down its LaunchAgent and bring it back when you finish; the agent lives under `$HOME/Library/LaunchAgents` under the project name. The MITM proxy daemon is separate, and a patched app needs it running to pass its launch preflight, so leave that one alone.

## Keep live config and repo defaults in sync

A change made only in the live config fixes the running machine but drifts from the repo. The repo encodes target defaults under `internal/targets` and a config fixture under `internal/testconfig/testdata`. When you change target behavior, change both, and update any test that pins the old behavior. When you change the config schema, update `config.example.toml` so the documented shape stays accurate.

## House rules

- Code discovery: prefer the semantic code search over the indexed repo, and fall back to ripgrep when results are low-signal.
- Git: fetch before any diff or range comparison, and compare against the remote-tracking ref.
- Prose: no em-dashes or other typographic dashes; the pre-commit gate blocks them.
- Commits: one imperative subject line stating what changed and where, with no body.
