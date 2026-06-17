package daemon

import (
	"context"
	"strings"
	"testing"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestRunPatchRejectsSameTargetConflict(t *testing.T) {
	setupConfig(t)
	exec := newExecutor()
	release := make(chan struct{})
	started := make(chan struct{})
	_, err := exec.startOrAttach(context.Background(), "upgrade", "demo", func(_ context.Context, _ func(*desktopviaclydev1.ProgressEvent)) error {
		close(started)
		<-release
		return nil
	})
	if err != nil {
		t.Fatalf("startOrAttach: %v", err)
	}
	<-started

	stream := newRecordingProgressStream(context.Background())
	err = newServer(exec, newUpdaterState()).RunPatch(
		&desktopviaclydev1.RunPatchRequest{Target: "demo"},
		stream,
	)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("RunPatch code = %v, want FailedPrecondition", status.Code(err))
	}
	for _, want := range []string{
		"active operation=upgrade",
		"active target=demo",
		"requested operation=patch",
		"requested target=demo",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("RunPatch error missing %q: %v", want, err)
		}
	}

	close(release)
}
