# Codex development signing and keychain access

This note records the durable signing constraints behind the Codex remote-control
enrollment fix. The current implementation lives in `internal/devsign`.

## Failure Class

Codex device-key enrollment writes to the process default keychain access group.
A re-signed app without a runtime `keychain-access-groups` entitlement fails
that write with `-34018`, which is `errSecMissingEntitlement`.

Stripping the entitlement keeps the app launchable but leaves enrollment broken.
Adding the entitlement without a matching provisioning profile makes macOS reject
the launch because `keychain-access-groups` is restricted.

Developer ID signing is not enough for this path. The overlay needs an Apple
Development signing profile that authorizes the team-scoped keychain group for
the local machine.

## Durable Shape

Development signing is opt-in target policy, not the default patch path.

When enabled for Codex, the patcher keeps the real Electron executable as
`CFBundleExecutable`. The shim and `.real` executable model is not used for this
target, because the provisioning profile must attach to the process that Launch
Services starts.

The patcher embeds the configured development provisioning profile, renders
entitlements from the configured signing team and bundle ID, and reseals the top
bundle with keychain-free `rcodesign --shallow`.

Nested vendor code stays on its upstream signature unless a target policy says
otherwise. The Codex Framework is not rewritten for proxy routing.

Proxy routing uses an external injector under the XDG state root. The patcher
writes an injector policy file, signs the dylib, verifies it, smoke-tests the
exact dylib and policy, moves it into place, removes any stale app-local
injector, and sets `LSEnvironment` to the external paths.

The injector clears `DYLD_INSERT_LIBRARIES` before child tools inherit the environment.

## Source of Truth

- `internal/devsign` owns the development-signing overlay.
- `internal/devsign/dev-entitlements.plist.tmpl` owns the rendered entitlement shape.
- `injector` owns the dylib source.
- `config.example.toml` documents the optional `development_signing` config block.
- `desktop-via-clyde status` reports the installed bundle state.
- Patch and upgrade logs under the XDG state root show the exact live commands
  and paths for a run.
