package daemon

import (
	"testing"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/operations"
)

type recordingProgress struct {
	steps         []string
	skips         []string
	fails         []string
	outcome       clioutput.Outcome
	outcomeDetail string
}

func (p *recordingProgress) Step(detail string) { p.steps = append(p.steps, detail) }
func (p *recordingProgress) Skip(detail string) { p.skips = append(p.skips, detail) }
func (p *recordingProgress) Fail(detail string) { p.fails = append(p.fails, detail) }
func (p *recordingProgress) SetOutcome(outcome clioutput.Outcome, detail string) {
	p.outcome = outcome
	p.outcomeDetail = detail
}

func TestApplyProgressEventDrivesStepAndOutcome(t *testing.T) {
	progress := &recordingProgress{}
	applyProgressEvent(&desktopviaclydev1.ProgressEvent{Type: string(clioutput.EventStepDone), Detail: "did a thing"}, progress)
	applyProgressEvent(&desktopviaclydev1.ProgressEvent{Type: string(clioutput.EventStepSkipped), Detail: "nothing to do"}, progress)
	detail, failed := applyProgressEvent(&desktopviaclydev1.ProgressEvent{
		Type:   string(clioutput.EventTargetDone),
		Status: string(clioutput.OutcomeOK),
		Detail: "done",
	}, progress)

	if failed {
		t.Fatal("ok target_done reported as failure")
	}
	if len(progress.steps) != 1 || progress.steps[0] != "did a thing" {
		t.Fatalf("steps = %v", progress.steps)
	}
	if len(progress.skips) != 1 || progress.skips[0] != "nothing to do" {
		t.Fatalf("skips = %v", progress.skips)
	}
	if progress.outcome != clioutput.OutcomeOK || progress.outcomeDetail != "done" {
		t.Fatalf("outcome = %q detail = %q", progress.outcome, progress.outcomeDetail)
	}
	if detail != "" {
		t.Fatalf("ok detail = %q, want empty", detail)
	}
}

func TestApplyProgressEventReportsFailure(t *testing.T) {
	progress := &recordingProgress{}
	detail, failed := applyProgressEvent(&desktopviaclydev1.ProgressEvent{
		Type:   string(clioutput.EventTargetDone),
		Status: string(clioutput.OutcomeFailed),
		Detail: "boom",
	}, progress)
	if !failed || detail != "boom" {
		t.Fatalf("failed = %v detail = %q, want true/boom", failed, detail)
	}
}

func TestFlagsRoundTripThroughProto(t *testing.T) {
	values := operations.NewFlagValues()
	values.SetString("channel", "dev")
	values.SetBool("dry-run", true)
	values.SetBool("migrate-keychain", false)

	restored := buildFlagValues(flagsToProto(values))
	if restored.String("channel") != "dev" {
		t.Fatalf("channel = %q, want dev", restored.String("channel"))
	}
	if !restored.Bool("dry-run") {
		t.Fatal("dry-run = false, want true")
	}
	if restored.Bool("migrate-keychain") {
		t.Fatal("migrate-keychain = true, want false")
	}
}

func TestIsDaemonStreamingCapability(t *testing.T) {
	streaming := []string{"app.upgrade", "app.patch", "app.hard-reset", "app.keychain-migrate"}
	for _, capability := range streaming {
		if !isDaemonStreamingCapability(capability) {
			t.Errorf("capability %q should be a daemon streaming capability", capability)
		}
	}
	for _, capability := range []string{"app.status", "standalone-cli.install", ""} {
		if isDaemonStreamingCapability(capability) {
			t.Errorf("capability %q should not be a daemon streaming capability", capability)
		}
	}
}
