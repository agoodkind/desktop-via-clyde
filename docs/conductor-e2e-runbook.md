# Conductor end-to-end runbook

This runbook verifies the full `desktop-via-clyde` lifecycle for the Conductor
target: `hard-reset`, `patch` (which installs a missing app via the updater),
and `upgrade`. A fresh agent can run every command here without other context.

The Conductor target is special: its app is a Tauri app fetched from CrabNebula
and signed with minisign, and it spawns native `claude`/`codex` CLIs plus a Bun
runtime. Patching installs a launch shim that routes the app's traffic through
the local clyde MITM proxy.

## Prerequisites

All of these are one-time and already true on the development machine. Verify,
do not reinstall.

1. **Repo + binary.** Build and install the CLI and its background updater daemon:
   ```sh
   cd ~/Sites/clyde-dev/desktop-via-clyde
   make install
   ```
   `make install` builds, signs, installs `~/.local/bin/desktop-via-clyde`, and
   restarts the updater daemon. The CLI is verb-first: `desktop-via-clyde <verb>
   conductor`, not `conductor <verb>`.

2. **Config.** `~/.config/desktop-via-clyde/config.toml` must contain an
   `[apps.conductor]` block with `[apps.conductor.updater] kind = "tauri_minisign"`
   and a `minisign_public_key`. Confirm:
   ```sh
   desktop-via-clyde status conductor
   ```
   This prints a `conductor` target row. If it errors with "unknown target", the
   config is missing the block.

3. **clyde MITM listener.** clyde must be running with the `app.conductor`
   listener bound on port 48731:
   ```sh
   lsof -nP -iTCP:48731 -sTCP:LISTEN
   ```
   Two `clyde ... (LISTEN)` lines (IPv4 + IPv6) are expected. If absent, deploy
   clyde with the conductor provider (`make deploy` in the clyde checkout).

4. **CA trust.** The `Clyde MITM CA` must be a trusted root so the app validates
   clyde's leaf certs:
   ```sh
   security dump-trust-settings -d 2>/dev/null | grep -A1 -i clyde
   ```

## Critical rule: never run patch/upgrade from inside Conductor

`patch`, `upgrade`, and `hard-reset` quit Conductor to mutate its bundle. Run
them from a standalone terminal. If your agent session runs inside a Conductor
workspace, the quit kills your session. Run from Terminal.app or an SSH shell.

## Step 1: hard-reset (force a clean slate)

`hard-reset` quits Conductor, wipes its macOS privacy grants, deletes
`/Applications/Conductor.app`, and clears the patch state record. It does not
redownload.

```sh
desktop-via-clyde hard-reset conductor
```

Expect `1 ok 0 failed`. Verify the app is gone:

```sh
desktop-via-clyde status conductor
# TARGET     STATE   NOTES
# conductor  absent  bundle missing at /Applications/Conductor.app
```

## Step 2: patch (installs the missing app, then shims it)

With the app absent, `patch` first runs the updater to download and install a
fresh Conductor, then patches it (installs the launch shim, re-signs with your
Developer ID, migrates the keychain grant).

```sh
desktop-via-clyde patch conductor --migrate-keychain
```

This downloads ~124 MB, so it takes a minute. Watch for these stages to all
report `ok` in the run log under
`~/.local/state/clyde/logs/patch/conductor-*.log`:

- `download ...`
- `verify minisign signature ... ok` is the minisign trusted-comment check
- `install shim ... -> .../MacOS/conductor`
- `re-sign with "Developer ID Application: ..."`
- `restored keychain access for 1 items`
- `verify bundle signature and shim dry-run ... ok` confirms the shim loads its policy

Expect `Result completed=1 failed=0`. Verify:

```sh
desktop-via-clyde status conductor
# conductor  patched  0.68.0  signed-as="Developer ID Application: ..."; upstream=27XN666UJ7
```

Confirm the shim loads its launch policy and injects the proxy env (no GUI
needed):

```sh
/Applications/Conductor.app/Contents/MacOS/conductor --clyde-dry-run
# would exec conductor.real
# policy: .../conductor.launch-policy.json
# env HTTPS_PROXY=http://localhost:48731
# ... exit 0
```

## Step 3: launch and confirm capture

```sh
open -a Conductor
sleep 12
pgrep -x conductor && pgrep -x conductor-runtime   # both must be running
```

No `desktop-via-clyde shim: Could not load launch policy` popup should appear.
Drive an agent in a Conductor workspace (run an LLM prompt), then confirm the
app's traffic is captured and decrypted by clyde:

```sh
DB="file:$HOME/.local/state/clyde/mitm/capture.db?mode=ro"
sqlite3 -separator '  ' "$DB" \
  "SELECT count(*), host, provider FROM requests \
   WHERE client='app.conductor' AND ts > strftime('%s','now')-300 \
   GROUP BY host, provider ORDER BY 1 DESC;"
```

Expect rows for `api.anthropic.com` (claude), `chatgpt.com` (codex), and
`*.cursor.sh` (the Cursor agent, captured under the separate `app.cursor`
listener). Note: Conductor does NOT contact `conductor.build` during normal use.
Its real backends are the model APIs and Cursor, all captured.

## Step 4: upgrade

`upgrade` fetches the latest manifest, and if a newer version exists, downloads,
minisign-verifies, swaps, and re-patches.

```sh
desktop-via-clyde upgrade conductor
```

When already on the latest version, expect `status=skipped` with the note
"already on latest". That is success, not failure. To exercise a real download,
verify, and install, run `hard-reset` first (Step 1), then `upgrade` (or `patch`,
which installs the missing app via the same path).

## Troubleshooting

- **`minisign trusted comment signature verification failed`**: the installed
  binary predates the trusted-comment fix. The global signature must be checked
  over the bare 64-byte signature, not the 74-byte blob. Rebuild from `main`
  with `make install`.

- **Popup `desktop-via-clyde shim: Could not load launch policy: data ... missing`**:
  the installed shim predates the null-collection fix. The shim must tolerate
  `"arguments": null` in the launch policy. Rebuild from `main` with
  `make install`, then re-run `patch conductor`.

- **`same-target conflict: active operation=upgrade`**: an operation is already
  running for conductor. Check `desktop-via-clyde updater status`, wait for the
  active run to finish, then retry.

- **Keychain prompt on launch**: macOS asks because the re-signed bundle has a
  new code hash. Click **Always Allow** so Conductor reads
  `com.conductor.app.production.settings` without prompting again for this hash.

- **`patch`/`upgrade` appears to hang at "downloading"**: the `tauri_minisign`
  updater streams the 124 MB asset without a progress bar. Confirm forward
  progress by sampling daemon throughput:
  `nettop -p "$(launchctl list | awk '/desktop-via-clyde.updater/{print $1}')" -x -l 1 -J bytes_in`.

## One-shot e2e

After the prerequisites hold, the full cycle is:

```sh
cd ~/Sites/clyde-dev/desktop-via-clyde && make install
desktop-via-clyde hard-reset conductor
desktop-via-clyde patch conductor --migrate-keychain
desktop-via-clyde status conductor          # -> patched 0.68.0
open -a Conductor
desktop-via-clyde upgrade conductor          # -> skipped (already latest)
```
