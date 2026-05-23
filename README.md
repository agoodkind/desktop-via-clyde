# desktop-via-clyde

`desktop-via-clyde` patches installed macOS Electron apps so their normal launches
route through the local Clyde MITM proxy at `http://[::1]:48723`.

The tool does not create wrapper apps. It mutates each target app bundle in
place, keeps the vendor executable as `<ExecName>.real`, installs a universal
Swift shim at the original executable path, re-signs the bundle, and records
enough state to re-apply the patch after app updates.

## Supported Targets

| Target | Bundle | Executable | Update source | Keychain services |
| --- | --- | --- | --- | --- |
| `cursor` | `/Applications/Cursor.app` | `Cursor` | Cursor JSON manifest | `Cursor Safe Storage` |
| `codex` | `/Applications/Codex.app` | `Codex` | Sparkle appcast at `https://persistent.oaistatic.com/codex-app-prod/appcast.xml` | `Codex Safe Storage`, `Codex Auth`, `Codex MCP Credentials` |
| `claude` | `/Applications/Claude.app` | `Claude` | Anthropic Squirrel JSON endpoint | `Claude Safe Storage` |

The canonical target registry lives in `internal/targets/targets.go`. Each
target defines the bundle path, executable name, bundle identifier, keychain
services, target-specific entitlements policy, nested code objects that must be
signed, and updater metadata.

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

Every launch prepends these Electron arguments before forwarding user-supplied
arguments:

```text
--proxy-server=http://[::1]:48723
--ignore-certificate-errors
```

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
SSL_CERT_FILE=<unset>
```

Codex has native custom CA support keyed by `CODEX_CA_CERTIFICATE`, so the shim
uses that Codex-specific variable and clears inherited `SSL_CERT_FILE` for the
Codex target.

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

`desktop-via-clyde patch codex` repairs the bundled helper, the active helper,
and every cached helper matching the plugin-cache path. This keeps Codex startup,
Codex plugin updates, and Codex app updates on the same signing and entitlement
policy.

The helper binary `Contents/MacOS/SkyComputerUseService` must trust the local
Developer ID team identifier `H3BMXM4W7H`, because the patched Codex app is
signed by `Developer ID Application: Alex Goodkind (H3BMXM4W7H)`.

The helper requirement file
`Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement`
must trust the same local Developer ID team identifier, because the nested
client verifies its parent process before accepting requests.

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

## Patch

Patch one app:

```bash
desktop-via-clyde patch cursor
desktop-via-clyde patch codex
desktop-via-clyde patch claude
```

Print the planned patch steps without changing the bundle:

```bash
desktop-via-clyde patch codex --dry-run
```

Skip LaunchAgent installation during isolated app-copy tests:

```bash
desktop-via-clyde patch codex --app-path /tmp/dvc-apps/Codex.app --skip-launch-agent
```

Skip keychain ACL re-granting when a smoke test should avoid keychain prompts:

```bash
desktop-via-clyde patch codex --no-migrate-keychain
```

The patch command is idempotent. Re-running it against an already-patched bundle
keeps `<ExecName>.real`, refreshes the shim, re-signs the needed code objects,
updates state, and verifies the result.

### Patch Flow

`patch <target>` runs these steps:

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
9. Re-sign target-specific nested code objects, the `.real` binary, the shim,
   and the outer `.app` bundle, using the target entitlement file for the
   `.real` binary, shim, and outer bundle seal.
10. Remove `com.apple.quarantine` from the bundle on a best-effort basis.
11. For Codex only, repair and locally re-sign the installed
    `$HOME/.codex/computer-use/Codex Computer Use.app` helper so its native
    trusted-sender policy accepts the locally signed Codex app. The helper patch
    strips Team-bound application-group entitlements, preserves Apple Events,
    and verifies the helper bundle signature.
12. For Codex only, repair and locally re-sign every cached helper matching
    `$HOME/.codex/plugins/cache/openai-bundled/computer-use/*/Codex Computer Use.app`
    so plugin updates keep the same helper policy.
13. Re-grant keychain ACLs on captured items so the re-signed app can keep using
    its existing secrets.
14. Write or update `state.json`.
15. Install and load the shared LaunchAgent watcher unless skipped.
16. Verify signatures with `codesign --verify --verbose=2` and verify required
    entitlement keys on the effective main executable.
17. Run `<ExecName> --clyde-dry-run` and print the resulting launch policy.

## Keychain Access

Re-signing changes the app identity that macOS keychain ACLs see. The patch flow
therefore captures target-specific generic-password items before re-signing and
re-grants access to the patched app afterward.

Run only the keychain capture and re-grant steps on an already-patched app:

```bash
desktop-via-clyde keychain-migrate cursor
desktop-via-clyde keychain-migrate codex
desktop-via-clyde keychain-migrate claude
```

The first patch or keychain re-grant for a target can still show macOS keychain
prompts. Choosing "Always Allow" makes later launches quiet for that keychain
item.

## Status

Print the state of every registered target:

```bash
desktop-via-clyde status
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

## Watcher

The watcher is a single user LaunchAgent:

```text
$HOME/Library/LaunchAgents/io.goodkind.desktop-via-clyde.watcher.plist
```

The watcher command is:

```bash
desktop-via-clyde watch
```

The watcher reads `state.json`, watches every target that has a state entry, and
uses a three-second debounce before checking drift. Drift means either
`<ExecName>.real` is missing or the current `CFBundleVersion` differs from the
version recorded at patch time. When drift is detected, the watcher sends a
desktop notification and runs the same `patch <target>` flow again.

Watcher logs are written to:

```text
$HOME/.local/state/clyde/desktop-via-clyde/watcher.log
```

## Upgrade

Upgrade one app by fetching the upstream update feed directly:

```bash
desktop-via-clyde upgrade cursor
desktop-via-clyde upgrade codex
desktop-via-clyde upgrade claude
```

`upgrade <target>` fetches the target's upstream manifest, downloads the full
`.app` update archive, verifies the extracted app against the original upstream
DesignatedRequirement recorded in `state.json`, swaps the verified bundle into
the app path, and then runs `patch <target>`.

Target-specific upgrade behavior:

| Target | Verification notes |
| --- | --- |
| `cursor` | Uses Cursor's JSON manifest and verifies the extracted app against the recorded upstream DesignatedRequirement. |
| `codex` | Uses the Sparkle appcast and verifies Sparkle Ed25519 signatures with the current or extracted `SUPublicEDKey`, then verifies the extracted app against the recorded upstream DesignatedRequirement. |
| `claude` | Uses Anthropic's Squirrel endpoint with a nonsecret generated `device_id`, then verifies the extracted app against the recorded upstream DesignatedRequirement. |

Print upgrade actions without replacing the app:

```bash
desktop-via-clyde upgrade codex --dry-run
```

Run an isolated upgrade smoke against a copied bundle and throwaway state root:

```bash
DESKTOP_VIA_CLYDE_STATE_ROOT=/tmp/dvc-state \
  desktop-via-clyde upgrade codex \
  --app-path /tmp/dvc-apps/Codex.app \
  --no-migrate-keychain \
  --skip-launch-agent
```

When launching a copied app with an isolated `HOME`, pass the real Clyde CA path
so the shim does not look for the CA under the temporary home directory:

```bash
DESKTOP_VIA_CLYDE_CA_CERT=/Users/agoodkind/.local/state/clyde/mitm/ca/clyde-mitm-ca.crt \
  /tmp/dvc-apps/Codex.app/Contents/MacOS/Codex --clyde-dry-run
```

## MITM Hook

The hidden command:

```bash
desktop-via-clyde mitm-hook patch-bundle <target>
```

is for Clyde MITM hook subprocesses. Clyde writes a JSON envelope on stdin for a
`transform_response` hook. The hook command reads the upstream response zip,
extracts the `.app` with `ditto`, verifies the upstream signature against the
recorded DesignatedRequirement, runs the shared bundle-mutation patch steps in a
temporary staging directory, re-zips the patched `.app`, and writes a JSON
response envelope telling Clyde to return the patched zip.

This path is used for update downloads that are intercepted by Clyde before the
app's own updater installs them. It shares the same bundle mutation code as
`patch`, but it does not write `state.json`, install the LaunchAgent, re-grant
keychain items, or run the final installed-app verification.

## Unpatch

Restore a target from its backup:

```bash
desktop-via-clyde unpatch cursor
desktop-via-clyde unpatch codex
desktop-via-clyde unpatch claude
```

`unpatch <target>` restores the saved bundle from:

```text
$HOME/Library/Application Support/desktop-via-clyde/backup/<target>/
```

It removes that target from `state.json` and verifies the restored signature.
The LaunchAgent is unloaded only when no other target remains patched.

## Files and State

| Path | Purpose |
| --- | --- |
| `bin/desktop-via-clyde` | Locally built CLI binary. |
| `$HOME/.local/bin/desktop-via-clyde` | Installed CLI binary. |
| `$HOME/Library/Application Support/desktop-via-clyde/state.json` | Per-target patched version, signing identity, patch time, and upstream DesignatedRequirement. |
| `$HOME/Library/Application Support/desktop-via-clyde/backup/<target>/` | Original upstream app bundle backup. |
| `$HOME/Library/LaunchAgents/io.goodkind.desktop-via-clyde.watcher.plist` | Shared watcher LaunchAgent. |
| `$HOME/.local/state/clyde/desktop-via-clyde/watcher.log` | Watcher log. |
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
| `cmd/desktop-via-clyde/` | Cobra CLI commands for patching, unpatching, watching, status, keychain access re-granting, upgrade, and MITM hook execution. |
| `internal/targets/` | Target registry and updater metadata. |
| `internal/patch/` | Patch, unpatch, keychain access re-granting, signing, state writing, and verification logic. |
| `internal/watch/` | FSEvents watcher and drift detection. |
| `internal/launchagent/` | LaunchAgent plist rendering. |
| `internal/state/` | `state.json` load and save code. |
| `internal/paths/` | Shared filesystem paths and signing identity. |
| `internal/upgrade/` | Upstream manifest fetch, archive verification, bundle swap, and post-upgrade patch flow. |
| `internal/embed/` | Embedded universal shim binary and shim dry-run tests. |
| `shim/` | Swift package for the self-locating MITM shim. |
