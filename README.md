# desktop-via-clyde

`desktop-via-clyde` patches installed macOS Electron apps so their normal launches
route through the local Clyde MITM proxy at `http://[::1]:48723`.

The tool does not create wrapper apps. It mutates each target app bundle in
place, keeps the vendor executable as `<ExecName>.real`, installs a universal
Swift shim at the original executable path, re-signs the bundle, and records
state for status checks and upgrade verification.

## Supported Targets

| Target | Bundle | Executable | Update source | Keychain services |
| --- | --- | --- | --- | --- |
| `cursor` | `/Applications/Cursor.app` | `Cursor` | Cursor JSON manifest | `Cursor Safe Storage` |
| `codex` | `/Applications/Codex.app` | `Codex` | Sparkle appcast at `https://persistent.oaistatic.com/codex-app-prod/appcast.xml` | `Codex Safe Storage`, `Codex Auth`, `Codex MCP Credentials` |
| `claude` | `/Applications/Claude.app` | `Claude` | Anthropic Squirrel JSON endpoint used by `desktop-via-clyde claude upgrade` | `Claude Safe Storage` |

The canonical target registry lives in `internal/targets/targets.go`. Each
target defines the bundle path, executable name, bundle identifier, keychain
services, target-specific entitlements policy, nested code objects that must be
signed, nested code objects that must keep upstream signatures, and updater
metadata.

## Runtime Model

After a target is patched, the app launch path looks like this:

```text
Finder, Dock, launchd, or open(1)
  -> /Applications/<App>.app/Contents/MacOS/<ExecName>
       This is the desktop-via-clyde Swift shim.
       It checks the Clyde CA file and proxy socket.
       It sets proxy arguments and target-specific environment variables.
       It execv(2)s the vendor binary.
  -> /Applications/<App>.app/Contents/MacOS/<ExecName>.real
       This is the original app executable.
       It receives the same argv[0] as the original executable name.
       It starts Electron and any child processes normally.
```

The shim is intentionally self-locating. It resolves its own path with
`_NSGetExecutablePath`, appends `.real` to find the vendor binary, and then
execs that sibling binary. This keeps the same shim binary usable for Cursor,
Codex, Claude, copied app bundles, and isolated smoke tests.

## Shim Behavior

The shim lives in `shim/Sources/Shim/main.swift`. The embedded copy used by the
Go binary is generated into `internal/embed/shim` by `make shim`.

Every launch uses this proxy URL:

```text
http://[::1]:48723
```

Normal Electron app launches prepend these Electron arguments before forwarding
user-supplied arguments:

```text
--proxy-server=http://[::1]:48723
--ignore-certificate-errors
```

Electron Node mode launches keep the original argument order and do not receive
the Electron proxy arguments. Electron Node mode is identified by
`ELECTRON_RUN_AS_NODE=1`, and Electron Node mode is used by editor command-line
entrypoints such as Cursor's `Contents/Resources/app/out/cli.js`.

The shim expects Clyde's MITM CA at:

```text
$HOME/.local/state/clyde/mitm/ca/clyde-mitm-ca.crt
```

`DESKTOP_VIA_CLYDE_CA_CERT` overrides that CA path. If `XDG_STATE_HOME` is set
and `DESKTOP_VIA_CLYDE_CA_CERT` is not set, the fallback CA path is:

```text
$XDG_STATE_HOME/clyde/mitm/ca/clyde-mitm-ca.crt
```

Before execing the real binary, the shim verifies that the CA file exists and
that `[::1]:48723` accepts a TCP connection. A failed preflight shows a macOS
alert and exits before the app starts.

### Default Environment Policy

Cursor, Claude, and any future target using the default policy receive:

```text
NODE_EXTRA_CA_CERTS=<clyde-ca>
SSL_CERT_FILE=<clyde-ca>
NODE_OPTIONS=--use-openssl-ca
NODE_TLS_REJECT_UNAUTHORIZED=0
HTTPS_PROXY=http://[::1]:48723
HTTP_PROXY=http://[::1]:48723
ALL_PROXY=http://[::1]:48723
NO_PROXY=localhost,127.0.0.1,::1,[::1]
no_proxy=localhost,127.0.0.1,::1,[::1]
```

### Codex Environment Policy

Codex receives a target-specific policy:

```text
CODEX_CA_CERTIFICATE=<clyde-ca>
NODE_EXTRA_CA_CERTS=<clyde-ca>
NODE_OPTIONS=--use-openssl-ca
NODE_TLS_REJECT_UNAUTHORIZED=0
HTTPS_PROXY=http://[::1]:48723
HTTP_PROXY=http://[::1]:48723
ALL_PROXY=http://[::1]:48723
NO_PROXY=localhost,127.0.0.1,::1,[::1]
no_proxy=localhost,127.0.0.1,::1,[::1]
SSL_CERT_FILE=<unset>
```

Codex has native custom CA support keyed by `CODEX_CA_CERTIFICATE`, so the shim
uses that Codex-specific variable and clears inherited `SSL_CERT_FILE` for the
Codex target.

`NO_PROXY` and `no_proxy` bypass the Clyde proxy only for loopback hosts. Local
probes such as `http://[::1]:5400` reach the local service directly, while
ordinary remote HTTP and HTTPS requests still use the Clyde MITM proxy.

## Codex Computer Use

Codex computer use is implemented by `Codex Computer Use.app`, a helper app with
bundle identifier `com.openai.sky.CUAService` that captures the screen and sends
Apple Events to other apps on behalf of Codex.

The Codex app bundle contains the bundled helper at:

```text
/Applications/Codex.app/Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app
```

The Codex runtime installs the active helper at:

```text
$HOME/.codex/computer-use/Codex Computer Use.app
```

The Codex plugin cache stores update candidates matching:

```text
$HOME/.codex/plugins/cache/openai-bundled/computer-use/*/Codex Computer Use.app
```

`desktop-via-clyde codex patch` repairs the bundled helper, the active helper,
and every cached helper matching the plugin-cache path. This keeps Codex startup,
Codex plugin updates, and Codex app updates on the same signing and entitlement
policy.

The helper binary `Contents/MacOS/SkyComputerUseService` must trust the local
Developer ID team identifier `H3BMXM4W7H`, because the patched Codex app is
signed by `Developer ID Application: Alex Goodkind (H3BMXM4W7H)`.

The helper requirement files
`Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement`
and
`Contents/SharedSupport/CUALockScreenGuardian.app/Contents/Resources/CUALockScreenGuardian_Parent.coderequirement`
must trust the same local Developer ID team identifier, because the nested
client and lock-screen guardian verify their parent process before accepting
requests.

The helper app entitlement `com.apple.security.automation.apple-events` permits
Apple Events, which are macOS messages used to control other applications.

The helper app entitlement `com.apple.security.device.audio-input` permits
microphone access, which satisfies the helper's macOS microphone permission
preflight.

The helper app entitlement `com.apple.security.application-groups` is stripped,
because application-group identifiers are bound to the upstream OpenAI team and
are invalid after local Developer ID signing.

The nested helper app
`Contents/SharedSupport/SkyComputerUseClient.app` keeps
`com.apple.security.automation.apple-events`, because the nested client sends or
receives Apple Events during computer-use control flows.

macOS stores privacy decisions in Transparency, Consent, and Control, abbreviated
TCC. The helper bundle requires these macOS privacy grants for normal operation:

| macOS privacy grant | TCC service name | Bundle identifier | Purpose |
| --- | --- | --- | --- |
| Accessibility | `kTCCServiceAccessibility` | `com.openai.sky.CUAService` | The helper can control the keyboard, pointer, and user interface. |
| Screen Recording | `kTCCServiceScreenCapture` | `com.openai.sky.CUAService` | The helper can capture screenshots for appshot, which is Codex screenshot capture for computer-use state. |
| Full Disk Access | `kTCCServiceSystemPolicyAllFiles` | `com.openai.sky.CUAService` | The helper can read app container data without repeated App Data prompts. |

Full Disk Access belongs on `Codex Computer Use.app`, not only on `Codex.app`,
because the helper process is the process that triggers macOS App Data access
checks.

Verify the active helper signature:

```bash
codesign --verify --deep --strict --verbose=2 "$HOME/.codex/computer-use/Codex Computer Use.app"
```

Verify the active helper entitlements:

```bash
codesign -d --entitlements :- "$HOME/.codex/computer-use/Codex Computer Use.app"
```

Verify the plugin-cache helper entitlements:

```bash
for app_path in "$HOME"/.codex/plugins/cache/openai-bundled/computer-use/*/"Codex Computer Use.app"; do
    codesign -d --entitlements :- "$app_path"
done
```

Use the shim dry run to inspect the exact launch policy without starting the
real app:

```bash
/Applications/Codex.app/Contents/MacOS/Codex --clyde-dry-run
/Applications/Cursor.app/Contents/MacOS/Cursor --clyde-dry-run
/Applications/Claude.app/Contents/MacOS/Claude --clyde-dry-run
```

## Install

Build and install the CLI:

```bash
cd /Users/agoodkind/Sites/clyde-dev/desktop-via-clyde
make install
```

`make install` builds the universal Mach-O shim, embeds it into the Go binary,
builds `bin/desktop-via-clyde`, and installs the CLI at:

```text
$HOME/.local/bin/desktop-via-clyde
```

The signing identity is fixed in `internal/paths/paths.go`:

```text
Developer ID Application: Alex Goodkind (H3BMXM4W7H)
```

At sign time the tool resolves that common name to a SHA-1 identity hash with
`security find-identity -v -p codesigning`, because the local keychain can hold
multiple certificates with the same common name.

## Codex CLI

Every program uses the same shape:

```text
desktop-via-clyde <program> <operation>
```

For example, `desktop-via-clyde codex-cli upgrade`, `desktop-via-clyde codex patch`,
and `desktop-via-clyde cursor upgrade`.

Build and install a locally signed Codex CLI from upstream source:

```bash
desktop-via-clyde codex-cli upgrade
```

`desktop-via-clyde codex-cli install` is preserved as an alias for `upgrade`.

The command clones `openai/codex` with this command when the managed cache is
missing:

```bash
gh repo clone openai/codex <cache> -- --depth 1
```

Existing caches are updated with `git fetch --depth 1 --prune origin main`,
and the checkout is detached at `FETCH_HEAD`.

The default source cache is:

```text
${XDG_CACHE_HOME:-$HOME/.cache}/desktop-via-clyde/codex/source
```

The build first resolves the upstream Rusty V8 artifact overrides, then runs
`cargo build --release --timings --bin codex` against the upstream workspace
for the host Darwin target. The installer signs that built `codex` binary with
the upstream Codex CLI entitlement file at
`.github/actions/macos-code-sign/codex.entitlements.plist` and the local
Developer ID identity from `internal/paths/paths.go`, then calls upstream
`scripts/build_codex_package.py` with `--entrypoint-bin` so the package staging
step reuses the already built and signed binary instead of rebuilding it.

When `sccache` is available and `RUSTC_WRAPPER` is not already set, the
installer enables it automatically for the Cargo build and prints the resulting
cache stats after the build.

When the same verified release for the current upstream HEAD and target is
already installed, the installer reuses that release and refreshes the visible
symlinks instead of rebuilding. Pass `--force-rebuild` to bypass that reuse.

`--build-mode local-fast` is the default, because the produced binary is the
everyday local Codex CLI. Local-fast keeps the same package and signing flow
while relaxing the entrypoint build to `lto=false` and a per-CPU
`codegen-units` override. Local-fast installs use a distinct release suffix so
they do not overwrite or masquerade as exact release builds for the same HEAD.

Pass `--build-mode release` to match the upstream release profile exactly; this
opts back in to the slower link-time-optimized build.

The installer prints each major phase and streams subprocess output from clone,
fetch, Cargo build, package build, signing, install, and verification commands.

The installed package uses the upstream standalone layout under:

```text
$CODEX_HOME/packages/standalone/releases/
```

The visible command is:

```text
$HOME/.local/bin/codex
```

Print the planned work without changing files:

```bash
desktop-via-clyde codex-cli upgrade --dry-run
```

Match the exact upstream release profile instead of the default local-fast
build:

```bash
desktop-via-clyde codex-cli upgrade --build-mode release
```

Inspect the local Codex CLI source, symlinks, signing, and PATH state:

```bash
desktop-via-clyde codex-cli status
```

## Patch

Patch one app:

```bash
desktop-via-clyde cursor patch
desktop-via-clyde codex patch
desktop-via-clyde claude patch
```

Print the planned patch steps without changing the bundle:

```bash
desktop-via-clyde codex patch --dry-run
```

Skip keychain ACL re-granting when a smoke test should avoid keychain prompts:

```bash
desktop-via-clyde codex patch --no-migrate-keychain
```

The patch command is idempotent. Re-running it against an already-patched bundle
keeps `<ExecName>.real`, refreshes the shim, re-signs the needed code objects,
updates state, and verifies the result.

### Patch Flow

`desktop-via-clyde <target> patch` runs these steps:

1. Read `Contents/Info.plist` to capture bundle version, bundle identifier, and
   executable name.
2. Copy the original `.app` into
   `$HOME/Library/Application Support/desktop-via-clyde/backup/<target>/`.
3. Capture configured keychain items for the target's `KeychainServices`.
4. Extract entitlements from `<ExecName>.real` when it exists, otherwise from
   the original executable path.
5. Apply the target-declared entitlements policy. All targets require
   `com.apple.security.cs.disable-library-validation`; Cursor and Codex require
   `com.apple.security.automation.apple-events`; Codex also strips Team-bound
   entitlement keys that cannot remain valid after re-signing with the local
   Developer ID.
6. Move `Contents/MacOS/<ExecName>` to `Contents/MacOS/<ExecName>.real` when
   the `.real` binary does not already exist.
7. Write the embedded Swift shim to `Contents/MacOS/<ExecName>`.
8. For Codex only, repair and locally re-sign the bundled
   `Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app`
   helper before sealing the outer app, so the installed helper receives the
   local signing and entitlement policy.
9. Restore target-specific nested code objects that must keep upstream
   signatures.
10. Re-sign target-specific nested code objects, the `.real` binary, the shim,
   and the outer `.app` bundle, using the target entitlement file for the
   `.real` binary, shim, and outer bundle seal.
11. Remove `com.apple.quarantine` from the bundle on a best-effort basis.
12. For Codex only, repair and locally re-sign the installed
    `$HOME/.codex/computer-use/Codex Computer Use.app` helper so its native
    trusted-sender policy accepts the locally signed Codex app. The helper patch
    strips Team-bound application-group entitlements, preserves Apple Events,
    and verifies the helper bundle signature.
13. For Codex only, repair and locally re-sign every cached helper matching
    `$HOME/.codex/plugins/cache/openai-bundled/computer-use/*/Codex Computer Use.app`
    so plugin updates keep the same helper policy.
14. Re-grant keychain ACLs on captured items so the re-signed app can keep using
    its existing secrets.
15. Write or update `state.json`.
16. Verify signatures with `codesign --verify --verbose=2` and verify required
    entitlement keys on the effective main executable.
17. Run `<ExecName> --clyde-dry-run` and print the resulting launch policy.

## Keychain Access

Re-signing changes the app identity that macOS keychain ACLs see. The patch flow
therefore captures target-specific generic-password items before re-signing and
re-grants access to the patched app afterward.

Run only the keychain capture and re-grant steps on an already-patched app:

```bash
desktop-via-clyde cursor keychain-migrate
desktop-via-clyde codex keychain-migrate
desktop-via-clyde claude keychain-migrate
```

The first patch or keychain re-grant for a target can still show macOS keychain
prompts. Choosing "Always Allow" makes later launches quiet for that keychain
item.

## Status

Print the state of every registered target:

```bash
desktop-via-clyde status
```

Print the state of one registered target:

```bash
desktop-via-clyde cursor status
desktop-via-clyde codex status
desktop-via-clyde claude status
```

The status command reads `state.json`, checks whether each bundle exists,
checks whether a target is recorded as patched, verifies that `<ExecName>.real`
exists, compares the current `CFBundleVersion` to the patched version, and
prints one of these labels:

| State | Meaning |
| --- | --- |
| `absent` | The bundle path does not exist. |
| `clean` | The bundle exists but has no state entry. |
| `patched` | The bundle has state, the `.real` binary exists, and the version matches. |
| `drifted` | The state entry exists, but the `.real` binary is missing or the bundle version changed. |

## Upgrade

Upgrade one app by fetching the upstream update feed directly:

```bash
desktop-via-clyde cursor upgrade
desktop-via-clyde codex upgrade
desktop-via-clyde claude upgrade
```

`desktop-via-clyde <target> upgrade` fetches the target's upstream manifest, downloads the full
`.app` update archive, verifies the extracted app against the original upstream
DesignatedRequirement recorded in `state.json`, swaps the verified bundle into
the app path, and then runs `desktop-via-clyde <target> patch`.

Target-specific upgrade behavior:

| Target | Verification notes |
| --- | --- |
| `cursor` | Uses Cursor's JSON manifest and verifies the extracted app against the recorded upstream DesignatedRequirement. |
| `codex` | Uses the Sparkle appcast and verifies Sparkle Ed25519 signatures with the current or extracted `SUPublicEDKey`, then verifies the extracted app against the recorded upstream DesignatedRequirement. |
| `claude` | Uses Anthropic's Squirrel endpoint with a nonsecret generated `device_id`, then verifies the extracted app against the recorded upstream DesignatedRequirement. A clean installed Claude app can supply the upstream DesignatedRequirement when no Claude entry exists in `state.json`. |

Print upgrade actions without replacing the app:

```bash
desktop-via-clyde codex upgrade --dry-run
```

Run an isolated upgrade smoke against a copied bundle and throwaway state root:

```bash
DESKTOP_VIA_CLYDE_STATE_ROOT=/tmp/dvc-state \
  desktop-via-clyde codex upgrade \
  --app-path /tmp/dvc-apps/Codex.app \
  --no-migrate-keychain
```

When launching a copied app with an isolated `HOME`, pass the real Clyde CA path
so the shim does not look for the CA under the temporary home directory:

```bash
DESKTOP_VIA_CLYDE_CA_CERT=/Users/agoodkind/.local/state/clyde/mitm/ca/clyde-mitm-ca.crt \
  /tmp/dvc-apps/Codex.app/Contents/MacOS/Codex --clyde-dry-run
```

Claude Desktop updates use `desktop-via-clyde claude upgrade`.

## Unpatch

Restore a target from its backup:

```bash
desktop-via-clyde cursor unpatch
desktop-via-clyde codex unpatch
desktop-via-clyde claude unpatch
```

`desktop-via-clyde <target> unpatch` restores the saved bundle from:

```text
$HOME/Library/Application Support/desktop-via-clyde/backup/<target>/
```

It removes that target from `state.json` and verifies the restored signature.

## Files and State

| Path | Purpose |
| --- | --- |
| `bin/desktop-via-clyde` | Locally built CLI binary. |
| `$HOME/.local/bin/desktop-via-clyde` | Installed CLI binary. |
| `$HOME/Library/Application Support/desktop-via-clyde/state.json` | Per-target patched version, signing identity, patch time, and upstream DesignatedRequirement. |
| `$HOME/Library/Application Support/desktop-via-clyde/backup/<target>/` | Original upstream app bundle backup. |
| `$HOME/.local/state/clyde/mitm/ca/clyde-mitm-ca.crt` | Clyde MITM CA certificate used by the shim. |
| `/Applications/<App>.app/Contents/MacOS/<ExecName>` | Installed shim after patching. |
| `/Applications/<App>.app/Contents/MacOS/<ExecName>.real` | Original vendor executable after patching. |

`DESKTOP_VIA_CLYDE_STATE_ROOT` overrides the Application Support state root for
isolated tests. `DESKTOP_VIA_CLYDE_CA_CERT` overrides the shim CA path for
runtime launch tests.

## Development

Build the embedded shim:

```bash
make shim
```

Build the CLI:

```bash
make build
```

Install the CLI:

```bash
make install
```

Run checks:

```bash
make check
```

`make check` rebuilds the embedded shim, verifies `gofmt`, runs `go vet ./...`,
and runs `go test ./...`.

Format Go code:

```bash
make fmt
```

Remove local build output and the embedded shim:

```bash
make clean
```

## Source Layout

| Path | Purpose |
| --- | --- |
| `cmd/desktop-via-clyde/` | Cobra CLI commands for target operations, aggregate status, and Codex CLI packaging. |
| `internal/codexcli/` | Codex CLI source checkout, upstream package build, local signing, standalone install, and status logic. |
| `internal/targets/` | Target registry and updater metadata. |
| `internal/patch/` | Patch, unpatch, keychain access re-granting, signing, state writing, and verification logic. |
| `internal/signing/` | Shared local Developer ID identity resolution and codesign argument helpers. |
| `internal/state/` | `state.json` load and save code. |
| `internal/paths/` | Shared filesystem paths and signing identity. |
| `internal/upgrade/` | Upstream manifest fetch, archive verification, bundle swap, and post-upgrade patch flow. |
| `internal/embed/` | Embedded universal shim binary and shim dry-run tests. |
| `shim/` | Swift package for the self-locating MITM shim. |
