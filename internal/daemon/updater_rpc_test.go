package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
)

type recordingProgressStream struct {
	ctx         context.Context
	mu          sync.Mutex
	events      []*desktopviaclydev1.ProgressEvent
	firstSendCh chan struct{}
	firstSend   sync.Once
}

func newRecordingProgressStream(ctx context.Context) *recordingProgressStream {
	return &recordingProgressStream{
		ctx:         ctx,
		firstSendCh: make(chan struct{}),
	}
}

func (stream *recordingProgressStream) SetHeader(metadata.MD) error  { return nil }
func (stream *recordingProgressStream) SendHeader(metadata.MD) error { return nil }
func (stream *recordingProgressStream) SetTrailer(metadata.MD)       {}
func (stream *recordingProgressStream) Context() context.Context     { return stream.ctx }
func (stream *recordingProgressStream) SendMsg(any) error            { return nil }
func (stream *recordingProgressStream) RecvMsg(any) error            { return nil }

func (stream *recordingProgressStream) Send(event *desktopviaclydev1.ProgressEvent) error {
	stream.mu.Lock()
	cloned, ok := proto.Clone(event).(*desktopviaclydev1.ProgressEvent)
	if !ok {
		stream.mu.Unlock()
		return errors.New("clone progress event: unexpected type")
	}
	stream.events = append(stream.events, cloned)
	stream.mu.Unlock()
	stream.firstSend.Do(func() {
		close(stream.firstSendCh)
	})
	return nil
}

func (stream *recordingProgressStream) snapshotTargets() []string {
	stream.mu.Lock()
	defer stream.mu.Unlock()
	targets := make([]string, 0, len(stream.events))
	for _, event := range stream.events {
		targets = append(targets, event.GetTarget())
	}
	return targets
}

func startActiveTestRun(t *testing.T, exec *executor, operation string, target string, release <-chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	_, err := exec.startOrAttach(context.Background(), operation, target, func(_ context.Context, emit func(*desktopviaclydev1.ProgressEvent)) error {
		emit(&desktopviaclydev1.ProgressEvent{
			Type:      string(clioutput.EventStepDone),
			Operation: operation,
			Target:    target,
			Detail:    target + " detail",
		})
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttach(%s, %s): %v", operation, target, err)
	}
	<-started
}

func TestGetUpdaterStatusReportsMultipleActiveRuns(t *testing.T) {
	exec := newExecutor()
	release := make(chan struct{})
	startActiveTestRun(t, exec, "upgrade", "codex", release)
	startActiveTestRun(t, exec, "upgrade", "claude", release)

	resp, err := newServer(exec, newUpdaterState()).GetUpdaterStatus(context.Background(), &desktopviaclydev1.GetUpdaterStatusRequest{})
	if err != nil {
		t.Fatalf("GetUpdaterStatus: %v", err)
	}
	if !resp.GetRunning() {
		t.Fatal("running = false, want true")
	}
	if resp.GetActiveTarget() != "claude" || resp.GetActiveOperation() != "upgrade" {
		t.Fatalf("legacy active run = %s/%s, want claude/upgrade", resp.GetActiveTarget(), resp.GetActiveOperation())
	}
	if len(resp.GetActiveRuns()) != 2 {
		t.Fatalf("active runs = %d, want 2", len(resp.GetActiveRuns()))
	}
	if resp.GetActiveRuns()[0].GetTarget() != "claude" || resp.GetActiveRuns()[1].GetTarget() != "codex" {
		t.Fatalf("active run order = %#v", resp.GetActiveRuns())
	}

	close(release)
}

func TestSubscribeActiveFiltersByTarget(t *testing.T) {
	exec := newExecutor()
	release := make(chan struct{})
	startActiveTestRun(t, exec, "upgrade", "codex", release)
	startActiveTestRun(t, exec, "upgrade", "codex-cli", release)

	stream := newRecordingProgressStream(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- newServer(exec, newUpdaterState()).SubscribeActive(
			&desktopviaclydev1.SubscribeActiveRequest{Target: "codex"},
			stream,
		)
	}()
	<-stream.firstSendCh
	close(release)

	if err := <-doneCh; err != nil {
		t.Fatalf("SubscribeActive(filtered): %v", err)
	}
	targets := stream.snapshotTargets()
	if len(targets) != 1 || targets[0] != "codex" {
		t.Fatalf("filtered targets = %v, want [codex]", targets)
	}
}

func TestSubscribeActiveWithoutFiltersStreamsEveryActiveRun(t *testing.T) {
	exec := newExecutor()
	release := make(chan struct{})
	startActiveTestRun(t, exec, "upgrade", "codex", release)
	startActiveTestRun(t, exec, "upgrade", "codex-cli", release)

	stream := newRecordingProgressStream(context.Background())
	doneCh := make(chan error, 1)
	go func() {
		doneCh <- newServer(exec, newUpdaterState()).SubscribeActive(
			&desktopviaclydev1.SubscribeActiveRequest{},
			stream,
		)
	}()
	<-stream.firstSendCh
	close(release)

	if err := <-doneCh; err != nil {
		t.Fatalf("SubscribeActive(unfiltered): %v", err)
	}
	targets := stream.snapshotTargets()
	if len(targets) != 2 {
		t.Fatalf("unfiltered targets = %v, want 2 events", targets)
	}
	targetSet := map[string]bool{}
	for _, target := range targets {
		targetSet[target] = true
	}
	if !targetSet["codex"] || !targetSet["codex-cli"] {
		t.Fatalf("unfiltered targets = %v, want codex and codex-cli", targets)
	}
}

func TestStatusRendersActiveRunsInTextAndJSON(t *testing.T) {
	originalRunLaunchctlStatus := runLaunchctlStatus
	originalDaemonReachableStatus := daemonReachableStatus
	originalFetchUpdaterStatusFn := fetchUpdaterStatusFn
	t.Cleanup(func() {
		runLaunchctlStatus = originalRunLaunchctlStatus
		daemonReachableStatus = originalDaemonReachableStatus
		fetchUpdaterStatusFn = originalFetchUpdaterStatusFn
	})

	runLaunchctlStatus = func(context.Context, ...string) (string, error) {
		return "", nil
	}
	daemonReachableStatus = func(context.Context) bool {
		return true
	}
	fetchUpdaterStatusFn = func(context.Context) (*desktopviaclydev1.GetUpdaterStatusResponse, error) {
		return &desktopviaclydev1.GetUpdaterStatusResponse{
			Running: true,
			ActiveRuns: []*desktopviaclydev1.ActiveRun{
				{Target: "codex", Operation: "upgrade"},
				{Target: "codex-cli", Operation: "upgrade"},
			},
		}, nil
	}

	var textOut bytes.Buffer
	if err := Status(context.Background(), &textOut, clioutput.FormatText); err != nil {
		t.Fatalf("Status(text): %v", err)
	}
	textBody := textOut.String()
	for _, want := range []string{
		"launch agent: loaded target=",
		"daemon rpc: responding socket=",
		"active run: target=codex operation=upgrade",
		"active run: target=codex-cli operation=upgrade",
	} {
		if !bytes.Contains(textOut.Bytes(), []byte(want)) {
			t.Fatalf("text status missing %q\noutput:\n%s", want, textBody)
		}
	}

	var jsonOut bytes.Buffer
	if err := Status(context.Background(), &jsonOut, clioutput.FormatJSON); err != nil {
		t.Fatalf("Status(json): %v", err)
	}
	var payload struct {
		LaunchAgent struct {
			Loaded bool `json:"loaded"`
		} `json:"launch_agent"`
		DaemonRPC struct {
			Responding bool `json:"responding"`
		} `json:"daemon_rpc"`
		Updater struct {
			ActiveRuns []struct {
				Target    string `json:"target"`
				Operation string `json:"operation"`
			} `json:"active_runs"`
		} `json:"updater"`
	}
	if err := json.Unmarshal(jsonOut.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal status json: %v\noutput:\n%s", err, jsonOut.String())
	}
	if !payload.LaunchAgent.Loaded || !payload.DaemonRPC.Responding {
		t.Fatalf("json status loaded/responding = %#v", payload)
	}
	if len(payload.Updater.ActiveRuns) != 2 {
		t.Fatalf("json active runs = %#v", payload.Updater.ActiveRuns)
	}
}
