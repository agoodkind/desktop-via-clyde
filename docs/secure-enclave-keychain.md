# Secure Enclave / keychain on re-signed Codex (the `-34018` enrollment failure)

This documents why the patched Codex "Control other devices" remote-enrollment
tab failed with `-34018`, why the obvious fixes do not work, and the proven
recipe that makes it work. It is the durable record for the keychain/Secure
Enclave investigation.

## Symptom

In a re-signed Codex, Settings → Connections → "Control other devices" → Set up →
Authorize failed with "Failed to authorize remote control". The app log showed:

```
remote_control_authorize_failed: Secure Enclave key creation failed
(hardware-backed device keys are not available on this platform);
OS-protected fallback failed (... OSStatus error -34018 - failed to add key to keychain)
```

`-34018` is `errSecMissingEntitlement`. The device-key native module
(`Contents/Resources/native/remote-control-device-key.node`) calls `SecItemAdd`
with no explicit access group, so the write lands in the process's default
keychain access group. The re-signed process holds no `keychain-access-groups`
entitlement at runtime, so the write is refused.

## Why the obvious fixes do not work

Verified live against this machine and App Store Connect (2026-06-05):

- **Self-asserting the entitlement fails.** amfi treats `keychain-access-groups`
  as a *restricted* entitlement that requires a matching provisioning profile,
  even for the app's own app-id group. Keeping it without a profile makes the app
  fail to launch: `amfid` logs `-413` "No matching profile found" / "Restricted
  entitlements not validated, bailing out", and `open` fails with NSPOSIXError
  163.
- **Stripping the entitlement keeps the app launchable but leaves enrollment
  broken** (`-34018`), because `SecItemAdd` has no group to write into.
- **A Developer ID provisioning profile does not help.** An embedded Developer ID
  profile does not inject team-restricted entitlements
  (`application-identifier`, `keychain-access-groups`) into the running process.
  `securityd` confirms it: with a Developer ID wildcard profile the app launches
  but `secd` logs `-34018` "Client has neither application-identifier nor
  application-groups nor keychain-access-groups entitlements". Apple also refuses
  to mint a Developer ID (Direct Distribution) profile from a wildcard App ID, and
  the explicit App ID `com.openai.codex.beta` is globally owned by OpenAI, so it
  cannot be registered under another team.

## What does work: an Apple Development cert + wildcard `MAC_APP_DEVELOPMENT` profile

A macOS **Development** provisioning profile does inject the restricted
entitlements into the running process (this is how Xcode debug builds get local
keychain sharing). Apple allows **wildcard** App IDs for Development profiles
(only Direct Distribution rejects wildcards), so no bundle-id rebrand and no
registration of OpenAI's App ID is needed.

Required, proven (a standalone test binary signed this way returned
`SecItemAdd OSStatus=0`, and Codex signed this way authorizes with no `-34018`):

- Sign with an **Apple Development** certificate (team `H3BMXM4W7H`).
- Embed a wildcard `MAC_APP_DEVELOPMENT` profile that authorizes
  `keychain-access-groups = [H3BMXM4W7H.*]` and registers this Mac as a
  development device.
- Entitlements on the **main** (LaunchServices-launched) executable:

  ```
  com.apple.application-identifier                 = H3BMXM4W7H.com.openai.codex.beta
  com.apple.developer.team-identifier              = H3BMXM4W7H
  keychain-access-groups                           = [ H3BMXM4W7H.* ]   (literal wildcard; must match the profile)
  com.apple.security.cs.allow-jit                  = true
  com.apple.security.cs.allow-unsigned-executable-memory = true
  com.apple.security.cs.disable-library-validation = true
  com.apple.security.automation.apple-events       = true
  ```

  No `get-task-allow` (it is restricted and the wildcard profile does not
  authorize it, so including it causes `-413`). The binary
  `keychain-access-groups` must be the literal `H3BMXM4W7H.*`; a concrete group
  there fails amfi validation against a wildcard profile.

## Critical implementation constraints

- **The main binary must be the LaunchServices-launched `CFBundleExecutable`.**
  The shim then `execv(.real)` model breaks this. amfi validates the embedded
  profile against the shim but cannot associate it with the `execv`'d real
  binary, so the device-key process `-413`s. Keep the real Electron binary as the
  main executable and drop the shim for this target.

- **Do NOT re-sign or modify the Codex Framework binary.** Re-signing or injecting
  a load command into `Contents/Frameworks/Codex Framework.framework/.../Codex
  Framework` makes the app crash about 7s after launch with `EXC_BREAKPOINT`
  (SIGTRAP) deep in V8/Node, even though `codesign --verify --deep --strict`
  passes and amfi accepts the launch. Leave the Framework and all nested helpers
  on their original Developer-ID signatures. Only the main executable and the
  bundle seal get the dev cert.

- **Re-seal the bundle with `rcodesign --shallow`.** It signs only the bundle's
  main executable and regenerates `CodeResources` while leaving nested code on its
  existing signatures. Exclude `Codex Computer Use.app`
  (`--exclude "<rel path>"`): rcodesign 0.29 otherwise descends into it and dies
  on its unsigned resource `.bundle`s ("binary does not have code signature
  data"). Also delete the node-hid `TestGUI.app.in` template, which is unsignable
  cruft that breaks the seal walker.

- **Sign keychain-free with `rcodesign`, never pollute the keychain.**
  `codesign --keychain <path> -s <sha1>` does NOT scope identity resolution to
  that keychain; it reads the search list. Using `codesign` would require adding
  the dev keychain to the search list, which breaks Xcode automatic signing for
  unrelated projects. `rcodesign` signs directly from a leaf-only `.p12` with no
  keychain at all. Pass a leaf-only p12 (`--p12-file ...`); a chained p12 makes
  rcodesign misdetect the team as the WWDR intermediate. See the keychain-safety
  rule in the project memory.

## clyde proxy routing without touching the Framework

Dropping the shim removes the mechanism that set the clyde MITM proxy. Re-add it
without modifying the Framework by loading the injector dylib via
`DYLD_INSERT_LIBRARIES` set in `Contents/Info.plist` `LSEnvironment`, gated by
adding `com.apple.security.cs.allow-dyld-environment-variables` to the main
entitlements (dyld ignores the var for a hardened-runtime process without it).
The injector lives outside the app bundle at
`$XDG_STATE_HOME/clyde/dev-signing/injectors/codex/c.dylib`, is signed with the
local Developer ID identity, and reads its policy from
`$XDG_STATE_HOME/clyde/dev-signing/injectors/codex/policy.bin`.

The injector's constructor runs before Chromium's `CommandLine::Init`. It first
unsets `DYLD_INSERT_LIBRARIES` so Codex-launched child tools do not inherit the
injection. It then applies the launch-policy env (proxy plus
`NODE_EXTRA_CA_CERTS` plus the ChatGPT backend base URL) and rewrites
`*_NSGetArgv()` to append `--proxy-server` and `--ignore-certificate-errors`,
skipping `--type=` child processes and `ELECTRON_RUN_AS_NODE`. The patcher must
smoke-test the exact external dylib and policy with a tiny host process before it
accepts the patched app.

## Verification (2026-06-05)

End-to-end through Computer Use, with clyde routing active: "Control other
devices" authorizes with no error (`-34018` gone), "Control this Mac" still lists
the live iPhone (mobile remote control healthy), the app is stable, and the
search list plus codesigning identities remain pristine. The dev cert lives only
in a dedicated keychain that is never added to the search list.
