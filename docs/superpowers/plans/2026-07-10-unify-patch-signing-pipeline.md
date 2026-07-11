# Unify the Patch Signing Pipeline Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task by task. Steps use checkbox syntax for tracking.

**Goal:** Route standard and development signing through one explicit patch pipeline, then verify Computer Use bundle repairs after the final strategy seal.

**Architecture:** `internal/patch` owns a strategy plan with preparation, seal timing, sealing, and verification. Named bundle extensions mutate the staged bundle before either seal and verify it after the final seal. Post-bundle hooks continue to repair installed helpers, caches, and system plugins.

**Tech Stack:** Go, macOS code signing, `rcodesign`, Graphite, and the existing shell-backed patch test harness.

## Global Constraints

- Keep CLI, TOML, updater, and signing-asset interfaces unchanged.
- Preserve standard and development signing behavior outside lifecycle ordering.
- Preserve staged-upgrade atomic rollback. Direct patch rollback remains out of scope.
- Use test-driven development and observe every new regression test fail before implementation.
- Use Graphite MCP for branch creation and submission. Do not use `git commit` or `git push`.

## Task 1: Unify signing strategy orchestration

**Produces:** `bundleSigningPlan`, standard and development strategy kinds, before-finalize and after-finalize seal phases, and one orchestrator for direct and staged patch flows.

- [ ] Add failing strategy selection tests for standard signing, development signing, and missing-asset standard fallback.
- [ ] Add failing phase trace tests proving standard seals before finalization and development seals after hooks.
- [ ] Add failing preparation, seal, and strategy verification tests proving patch state is not written.
- [ ] Add failing staged finalization coverage proving the previous bundle is restored.
- [ ] Implement the internal plan and selector without changing external interfaces.
- [ ] Remove the development-signing early return and route both strategies through the shared orchestrator.
- [ ] Preserve standard preparation and development shallow resealing behavior.
- [ ] Run focused patch, upgrade, development-signing, and keychain tests.
- [ ] Complete requirements and code-quality reviews, then create `codex/signing-pipeline-orchestrator` with Graphite.

## Task 2: Add the bundle extension contract

**Consumes:** The strategy phases from Task 1.

**Produces:** Named `MutateBeforeSeal` and `VerifyAfterSeal` extension callbacks executed in stable order.

- [ ] Add failing tests proving each strategy invokes one mutation and one verifier.
- [ ] Add failing tests proving stable name order and final-seal-before-verification order.
- [ ] Add failing mutation and extension verification tests proving patch state is not written.
- [ ] Replace `RegisterPreResignHook` with the paired internal extension registration.
- [ ] Migrate Computer Use registration by separating its existing bundled mutation and verification behavior.
- [ ] Keep post-bundle hooks separate and preserve their existing timing.
- [ ] Run focused patch, upgrade, extension, and Computer Use tests.
- [ ] Complete requirements and code-quality reviews, then create `codex/bundle-extension-contract` with Graphite.

## Task 3: Harden Computer Use verification

**Consumes:** The paired extension contract from Task 2.

**Produces:** Exact bundled-helper mutation and post-seal verification for identities, entitlements, trusted bytes, and parent requirements.

- [ ] Add failing exact trace tests for the authorization plugin, installer, client, guardian, outer helper, and final strategy seal.
- [ ] Add failing tests for every required and stripped entitlement rule.
- [ ] Add failing tests for upstream team bytes and both parent requirement plists.
- [ ] Implement bundled mutation and post-seal verification against the configured local team.
- [ ] Require bundled mutation and verification before accepting installed-helper and cache repair traces.
- [ ] Pin standard Codex, Claude, Cursor, Conductor, proxy injection, preserved code, keychain, direct, staged, and dry-run compatibility.
- [ ] Run `make check` and a final full-stack requirements and code-quality review.
- [ ] Create `codex/computer-use-signing-verification` and submit the stack with Graphite.

## Task 4: Perform live acceptance

- [ ] Build and install the top-branch `desktop-via-clyde`.
- [ ] Patch the live Codex bundle with the current live configuration.
- [ ] Verify bundled, cached, and installed helpers use the configured local team.
- [ ] Verify both parent requirement plists trust the configured local team.
- [ ] Restart Codex with Xcode open on a project and attach an Xcode App Snapshot.
- [ ] Confirm a settled capture contains screenshot or accessibility content.
- [ ] Confirm no new Apple Event `-10000` appears.
- [ ] Confirm `desktop-via-clyde status --output-format json` is healthy.
- [ ] Do not reset TCC unless a distinct denial appears after signature verification.
