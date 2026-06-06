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
	progress := session.TargetProgress("codex")
	progress.Step("prepared shim")
	progress.Skip("skipped relaunch")
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

// TestLiveModelSubStepSkipDoesNotChangeTargetOK is the regression guard for the
// core bug: a skipped (or failed) sub-step must never set the target's terminal
// status. Only the authoritative EventTargetDone does.
func TestLiveModelSubStepSkipDoesNotChangeTargetOK(t *testing.T) {
	model := newLiveModel()
	model.apply(runStarted())
	model.apply(targetStarted("cursor"))

	skip := NewEvent(EventStepSkipped, "upgrade")
	skip.Target = "cursor"
	skip.Status = statusSkipped
	skip.Detail = "target=cursor skipped nested entitlements identifier com.github.Electron.framework"
	model.apply(skip)

	done := NewEvent(EventTargetDone, "upgrade")
	done.Target = "cursor"
	done.Status = statusOK
	done.Detail = "target=cursor upgrade to 1.11187.4 complete"
	model.apply(done)

	assertRowState(t, &model, "cursor", statusOK)
	if detail := rowDetail(t, &model, "cursor"); !strings.Contains(detail, "upgrade to 1.11187.4 complete") {
		t.Fatalf("cursor detail = %q, want upgrade-complete text", detail)
	}
}

func TestLiveModelTargetDoneSkippedRendersSkipped(t *testing.T) {
	model := newLiveModel()
	model.apply(runStarted())
	model.apply(targetStarted("cursor"))
	done := NewEvent(EventTargetDone, "upgrade")
	done.Target = "cursor"
	done.Status = statusSkipped
	done.Detail = "target=cursor already on version 3.7.6; nothing to do"
	model.apply(done)

	assertRowState(t, &model, "cursor", statusSkipped)
}

func TestLiveModelTargetDoneFailureRendersFailed(t *testing.T) {
	model := newLiveModel()
	model.apply(runStarted())
	model.apply(targetStarted("codex"))
	done := NewEvent(EventTargetDone, "upgrade")
	done.Target = "codex"
	done.Status = statusFailed
	done.Detail = "patch clean bundle after version check: boom"
	model.apply(done)

	assertRowState(t, &model, "codex", statusFailed)
	if detail := rowDetail(t, &model, "codex"); !strings.Contains(detail, "boom") {
		t.Fatalf("codex detail = %q, want failure text", detail)
	}
}

// TestLiveModelAggregateMatchesTerminalStatuses verifies the aggregate counts
// come from the per-target terminal statuses and that ok+skipped+failed equals
// the target count.
func TestLiveModelAggregateMatchesTerminalStatuses(t *testing.T) {
	model := newLiveModel()
	model.apply(runStarted())
	for _, id := range []string{"claude", "codex", "cursor"} {
		model.apply(targetStarted(id))
	}
	applyDone(&model, "claude", statusOK)
	applyDone(&model, "codex", statusSkipped)
	applyDone(&model, "cursor", statusFailed)
	model.apply(NewEvent(EventRunDone, "upgrade"))

	okCount, skippedCount, failedCount, activeCount := model.targetCounts()
	if okCount != 1 || skippedCount != 1 || failedCount != 1 || activeCount != 0 {
		t.Fatalf("counts ok=%d skipped=%d failed=%d active=%d, want 1/1/1/0", okCount, skippedCount, failedCount, activeCount)
	}
	if okCount+skippedCount+failedCount != len(model.targets) {
		t.Fatalf("terminal counts sum %d != target count %d", okCount+skippedCount+failedCount, len(model.targets))
	}
	output := model.View()
	for _, want := range []string{"1 ok", "1 skipped", "1 failed", "finished"} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
}

func runStarted() Event {
	event := NewEvent(EventRunStarted, "upgrade")
	event.Time = "2026-06-06T08:40:43-07:00"
	event.RunLog = "/tmp/run.jsonl"
	return event
}

func targetStarted(target string) Event {
	event := NewEvent(EventTargetStarted, "upgrade")
	event.Target = target
	event.Status = statusRunning
	event.LogFile = "/tmp/" + target + ".log"
	return event
}

func applyDone(model *liveModel, target string, status string) {
	event := NewEvent(EventTargetDone, "upgrade")
	event.Target = target
	event.Status = status
	model.apply(event)
}

func assertRowState(t *testing.T, model *liveModel, target string, wantStatus string) {
	t.Helper()
	for _, row := range model.tableRows() {
		if row.Target != target {
			continue
		}
		if row.State != wantStatus {
			t.Fatalf("row %s state = %q, want %q", target, row.State, wantStatus)
		}
		return
	}
	t.Fatalf("missing row for target %s\nview:\n%s", target, model.View())
}

func rowDetail(t *testing.T, model *liveModel, target string) string {
	t.Helper()
	for _, row := range model.tableRows() {
		if row.Target == target {
			return row.Detail
		}
	}
	t.Fatalf("missing row for target %s", target)
	return ""
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
