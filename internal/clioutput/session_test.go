package clioutput

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSessionJSONEmitsTypedRunEvents(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var out bytes.Buffer
	session, err := NewSession(context.Background(), SessionOptions{
		Out:       &out,
		Format:    FormatJSON,
		Operation: "patch",
		Scope:     "codex",
		Parallel:  1,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	writer := session.ProgressWriter("codex")
	if _, err := writer.Write([]byte("[run] prepared shim\n")); err != nil {
		t.Fatalf("Write prepared shim: %v", err)
	}
	if _, err := writer.Write([]byte("[run] skipped relaunch\n")); err != nil {
		t.Fatalf("Write skipped relaunch: %v", err)
	}
	if err := session.Close([]TargetResult{NewTargetResult("codex", "app", nil, 0)}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 6 {
		t.Fatalf("line count = %d, want 6\noutput:\n%s", len(lines), out.String())
	}
	assertJSONEventType(t, lines[0], EventRunStarted)
	assertJSONEventType(t, lines[1], EventStepStarted)
	assertJSONEventType(t, lines[2], EventStepDone)
	assertJSONEventType(t, lines[3], EventStepStarted)
	assertJSONEventType(t, lines[4], EventStepSkipped)
	assertJSONEventType(t, lines[5], EventRunDone)
}

func TestSessionTextRendersCoherentNonTTYLines(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	var out bytes.Buffer
	session, err := NewSession(context.Background(), SessionOptions{
		Out:       &out,
		Format:    FormatText,
		Operation: "patch",
		Scope:     "all",
		Parallel:  2,
		DryRun:    false,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	event := NewEvent(EventTargetQueued, "patch")
	event.Target = "codex"
	event.Status = "queued"
	if err := session.Emit(event); err != nil {
		t.Fatalf("Emit queued: %v", err)
	}
	if err := session.Close([]TargetResult{NewTargetResult("codex", "app", nil, 0)}); err != nil {
		t.Fatalf("Close: %v", err)
	}

	output := out.String()
	for _, want := range []string{"Patch all", "run log", "codex queued", "Result completed=1 failed=0"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, output)
		}
	}
}

func TestLiveModelKeepsSkippedStatusAfterTargetDone(t *testing.T) {
	model := newLiveModel()
	started := NewEvent(EventRunStarted, "upgrade")
	started.Target = "cursor"
	started.RunLog = "/tmp/run.jsonl"
	model.apply(started)
	targetStarted := NewEvent(EventTargetStarted, "upgrade")
	targetStarted.Target = "cursor"
	targetStarted.LogFile = "/tmp/cursor.log"
	model.apply(targetStarted)
	applyDoneStep(&model, "cursor", "current_version_3_7_6", "target=cursor current version=3.7.6 channel=dev updater=cursor")
	skipped := NewEvent(EventStepSkipped, "upgrade")
	skipped.Target = "cursor"
	skipped.Step = "no_update_available"
	skipped.Status = statusSkipped
	skipped.Detail = "target=cursor no update available on dev channel; nothing to do"
	model.apply(skipped)
	done := NewEvent(EventTargetDone, "upgrade")
	done.Target = "cursor"
	done.Status = statusOK
	model.apply(done)

	output := model.View()
	for _, want := range []string{"Upgrade", "running", "1 target", "cursor", "skipped", "no update available", "current version: 3.7.6"} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	if strings.Contains(output, "dev channel") {
		t.Fatalf("view contains raw updater no-update detail\nview:\n%s", output)
	}
	if strings.Contains(output, "100%") {
		t.Fatalf("view contains fake progress percentage\nview:\n%s", output)
	}
}

func TestLiveModelRendersAggregateUpgradeTableAndLogs(t *testing.T) {
	model := newLiveModel()
	started := NewEvent(EventRunStarted, "upgrade")
	started.Time = "2026-06-03T08:40:43-07:00"
	started.RunLog = "/Users/agoodkind/.local/state/clyde/logs/upgrade/all-20260603T084043.jsonl"
	model.apply(started)
	applyStartedTarget(&model, "claude")
	applyStartedTarget(&model, "codex")
	applyStartedTarget(&model, "codex-cli")
	applyStartedTarget(&model, "cursor")
	applyDoneStep(&model, "claude", "upgrade_complete", "target=claude upgrade to 1.10628.0 complete")
	applyTargetDone(&model, "claude", statusOK, "", "")
	applySkippedStep(&model, "codex", "already_on_version", "target=codex already on version 3436; nothing to do")
	applyTargetDone(&model, "codex", statusOK, "", "")
	applyStartedStep(&model, "codex-cli", "building_codex_entrypoint", "codex-cli: building Codex entrypoint still running after 30s")
	applyDoneStep(&model, "cursor", "upgrade_complete", "target=cursor upgrade to 3.7.6 complete")
	applyTargetDone(&model, "cursor", statusOK, "", "")

	output := model.View()
	for _, want := range []string{
		"run log    /Users/agoodkind/.local/state/clyde/logs/upgrade/all-20260603T084043.jsonl",
		"Upgrade    running    4 targets    started 08:40:43",
		"TARGET",
		"STATE",
		"STEP",
		"DETAIL",
		"claude",
		"ok",
		"upgrade complete",
		"upgraded to 1.10628.0",
		"codex",
		"skipped",
		"no update available",
		"current version: 3436",
		"codex-cli",
		"running",
		"building codex entrypoint",
		"still running after 30s",
		"cursor",
		"upgraded to 3.7.6",
		"Logs",
		"/Users/agoodkind/.local/state/clyde/logs/upgrade/claude-20260603T084043.log",
		"/Users/agoodkind/.local/state/clyde/logs/upgrade/codex-20260603T084043.log",
		"/Users/agoodkind/.local/state/clyde/logs/upgrade/codex-cli-20260603T084043.log",
		"/Users/agoodkind/.local/state/clyde/logs/upgrade/cursor-20260603T084043.log",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	runLogIndex := strings.Index(output, "run log    ")
	summaryIndex := strings.Index(output, "Upgrade    running")
	if runLogIndex == -1 || summaryIndex == -1 || runLogIndex > summaryIndex {
		t.Fatalf("run log should render before summary\nview:\n%s", output)
	}
	logsIndex := strings.Index(output, "Logs")
	if logsIndex == -1 {
		t.Fatalf("view missing Logs section\nview:\n%s", output)
	}
	mainTable := output[:logsIndex]
	for _, blocked := range []string{
		"/Users/agoodkind/.local/state/clyde/logs/upgrade/claude-20260603T084043.log",
		"/Users/agoodkind/.local/state/clyde/logs/upgrade/codex-20260603T084043.log",
		"/Users/agoodkind/.local/state/clyde/logs/upgrade/codex-cli-20260603T084043.log",
		"/Users/agoodkind/.local/state/clyde/logs/upgrade/cursor-20260603T084043.log",
	} {
		if strings.Contains(mainTable, blocked) {
			t.Fatalf("main table contains target log path %q\nview:\n%s", blocked, output)
		}
	}
	for _, blocked := range []string{"Active", "Failures"} {
		if strings.Contains(output, blocked) {
			t.Fatalf("view contains removed section %q\nview:\n%s", blocked, output)
		}
	}
}

func TestLiveModelAlreadyCurrentSuccessRendersInstalledVersion(t *testing.T) {
	model := newLiveModel()
	applyStartedTarget(&model, "codex")
	applySkippedStep(&model, "codex", "already_on_version", "target=codex already on version 3436; nothing to do")
	applyTargetDone(&model, "codex", statusOK, "", "")

	output := model.View()
	for _, want := range []string{"codex", "skipped", "no update available", "current version: 3436"} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	if strings.Contains(output, "already on version") {
		t.Fatalf("view contains raw already-current detail\nview:\n%s", output)
	}
}

func TestLiveModelUpdaterNoUpdateUsesLastObservedCurrentVersion(t *testing.T) {
	model := newLiveModel()
	applyStartedTarget(&model, "cursor")
	applyDoneStep(&model, "cursor", "current_version_3_7_6", "target=cursor current version=3.7.6 channel=dev updater=cursor")
	applySkippedStep(&model, "cursor", "no_update_available", "target=cursor no update available on dev channel; nothing to do")
	applyTargetDone(&model, "cursor", statusOK, "", "")

	output := model.View()
	for _, want := range []string{"cursor", "skipped", "no update available", "current version: 3.7.6"} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	for _, blocked := range []string{"dev channel", "nothing to do"} {
		if strings.Contains(output, blocked) {
			t.Fatalf("view contains raw updater no-update detail %q\nview:\n%s", blocked, output)
		}
	}
}

func TestLiveModelTargetDoneFailureOverridesAlreadyCurrentProgress(t *testing.T) {
	model := newLiveModel()
	applyStartedTarget(&model, "codex")
	applySkippedStep(&model, "codex", "already_on_version", "target=codex already on version 3436; nothing to do")
	applyTargetDone(&model, "codex", statusFailed, "operation_failed", "patch clean bundle after version check: boom")

	output := model.View()
	for _, want := range []string{"codex", "failed", "operation failed", "patch clean bundle after version check: boom"} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	for _, blocked := range []string{"skipped", "current version: 3436"} {
		if strings.Contains(output, blocked) {
			t.Fatalf("view contains stale already-current state %q\nview:\n%s", blocked, output)
		}
	}
}

func TestLiveModelTrimsDuplicateRunningStepDetail(t *testing.T) {
	model := newLiveModel()
	applyStartedTarget(&model, "codex-cli")
	applyStartedStep(
		&model,
		"codex-cli",
		"building_codex_entrypoint_still_running_after_21m30s",
		"codex-cli: building Codex entrypoint still running after 21m30s",
	)

	output := model.View()
	for _, want := range []string{"codex-cli", "running", "building codex entrypoint", "still running after 21m30s"} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	if strings.Count(strings.ToLower(output), "building codex entrypoint") != 1 {
		t.Fatalf("view repeats running step text\nview:\n%s", output)
	}
	if strings.Contains(output, "building codex entrypoint still running after 21m30s") {
		t.Fatalf("view leaves elapsed detail in step column\nview:\n%s", output)
	}
}

func TestLiveModelOmitsExactDuplicateStepDetail(t *testing.T) {
	model := newLiveModel()
	applyStartedTarget(&model, "claude")
	applyStartedStep(&model, "claude", "starting", "target=claude starting")

	output := model.View()
	if strings.Count(output, "starting") != 1 {
		t.Fatalf("view repeats exact step detail\nview:\n%s", output)
	}
}

func TestLiveModelCompactsSccacheAndFreshBundleDetails(t *testing.T) {
	model := newLiveModel()
	applyStartedTarget(&model, "codex-cli")
	applyStartedStep(
		&model,
		"codex-cli",
		"using_sccache_wrapper_opt_homebrew_bin_sccache",
		"codex-cli: using sccache wrapper /opt/homebrew/bin/sccache",
	)
	applyStartedTarget(&model, "claude")
	applyStartedStep(
		&model,
		"claude",
		"installing_fresh_bundle",
		"target=claude installing fresh bundle /Users/agoodkind/.local/state/clyde/upgrade-staging/claude-1.10628.2-1780529790/extracted/Claude.app -> /Applications/Claude.app",
	)

	output := model.View()
	for _, want := range []string{
		"using sccache",
		"/opt/homebrew/bin/sccache",
		"installing fresh bundle",
		"replacing Claude.app",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	for _, blocked := range []string{
		"using sccache wrapper",
		"upgrade-staging",
		"extracted/Claude.app",
	} {
		if strings.Contains(output, blocked) {
			t.Fatalf("view contains noisy detail %q\nview:\n%s", blocked, output)
		}
	}
}

func TestLiveModelCompactsVersionInstallAndToolchainDetails(t *testing.T) {
	model := newLiveModel()
	applyStartedTarget(&model, "codex")
	applyStartedStep(
		&model,
		"codex",
		"current_version_3436_channel_beta",
		"target=codex current version=3436 channel=beta updater=sparkle_appcast",
	)
	applyStartedTarget(&model, "codex-cli")
	applyStartedStep(
		&model,
		"codex-cli",
		"install_complete_release_users_agoodkind_codex_packages_standalone_releases_dryrun_main_dryrun_aarch64_apple_darwin_local_fast",
		"codex-cli: install complete release=/Users/agoodkind/.codex/packages/standalone/releases/dryrun/main/dryrun-aarch64-apple-darwin/local-fast local_fast=true",
	)
	applyStartedTarget(&model, "rust")
	applyStartedStep(
		&model,
		"rust",
		"installing_or_updating_upstream_rust_toolchain_from_users_agoodkind_cache_clyde_desktop_via_clyde_codex_source_codex_rs_rust_toolchain_toml",
		"installing or updating upstream Rust toolchain from /Users/agoodkind/.cache/clyde/desktop-via-clyde/codex/source/codex-rs/rust-toolchain.toml",
	)

	output := model.View()
	for _, want := range []string{
		"checking current version",
		"current version: 3436",
		"install complete",
		"release: local-fast",
		"installing rust toolchain",
		"from rust-toolchain.toml",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	for _, blocked := range []string{
		"current version 3436 channel beta",
		"channel=beta",
		"install complete release",
		"standalone/releases",
		"installing or updating upstream Rust toolchain",
		"codex/source/codex-rs",
	} {
		if strings.Contains(output, blocked) {
			t.Fatalf("view contains duplicate or noisy detail %q\nview:\n%s", blocked, output)
		}
	}
}

func applyStartedTarget(model *liveModel, target string) {
	event := NewEvent(EventTargetStarted, "upgrade")
	event.Target = target
	event.Status = statusRunning
	event.LogFile = "/Users/agoodkind/.local/state/clyde/logs/upgrade/" + target + "-20260603T084043.log"
	model.apply(event)
}

func applyStartedStep(model *liveModel, target string, step string, detail string) {
	event := NewEvent(EventStepStarted, "upgrade")
	event.Target = target
	event.Step = step
	event.Status = statusRunning
	event.Detail = detail
	event.LogFile = "/Users/agoodkind/.local/state/clyde/logs/upgrade/" + target + "-20260603T084043.log"
	model.apply(event)
}

func applyDoneStep(model *liveModel, target string, step string, detail string) {
	event := NewEvent(EventStepDone, "upgrade")
	event.Target = target
	event.Step = step
	event.Status = statusOK
	event.Detail = detail
	event.LogFile = "/Users/agoodkind/.local/state/clyde/logs/upgrade/" + target + "-20260603T084043.log"
	model.apply(event)
}

func applySkippedStep(model *liveModel, target string, step string, detail string) {
	event := NewEvent(EventStepSkipped, "upgrade")
	event.Target = target
	event.Step = step
	event.Status = statusSkipped
	event.Detail = detail
	event.LogFile = "/Users/agoodkind/.local/state/clyde/logs/upgrade/" + target + "-20260603T084043.log"
	model.apply(event)
}

func applyTargetDone(model *liveModel, target string, status string, step string, detail string) {
	event := NewEvent(EventTargetDone, "upgrade")
	event.Target = target
	event.Status = status
	event.Step = step
	event.Detail = detail
	event.LogFile = "/Users/agoodkind/.local/state/clyde/logs/upgrade/" + target + "-20260603T084043.log"
	model.apply(event)
}

func assertJSONEventType(t *testing.T, line string, want EventType) {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("unmarshal event: %v\nline:\n%s", err, line)
	}
	if payload["type"] != string(want) {
		t.Fatalf("event type = %#v, want %#v\nline:\n%s", payload["type"], want, line)
	}
}
