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
	for _, want := range []string{"Upgrade  running  1 target", "cursor", "skipped", "no update available", "dev channel"} {
		if !strings.Contains(output, want) {
			t.Fatalf("view missing %q\nview:\n%s", want, output)
		}
	}
	if strings.Contains(output, "100%") {
		t.Fatalf("view contains fake progress percentage\nview:\n%s", output)
	}
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
