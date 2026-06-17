package daemon

import (
	"bytes"
	"context"
	"strings"
	"testing"

	desktopviaclydev1 "goodkind.io/desktop-via-clyde/api/desktopviaclyde/v1"
)

func TestWriteStatusTextIncludesActiveRuns(t *testing.T) {
	snapshot := statusSnapshot{
		LaunchAgent: launchAgentStatus{Loaded: true, Target: "gui/501/io.goodkind.desktop-via-clyde.updater"},
		DaemonRPC:   daemonRPCStatus{Responding: true, Socket: "/tmp/daemon.sock"},
		Updater: &desktopviaclydev1.GetUpdaterStatusResponse{
			ActiveRuns: []*desktopviaclydev1.ActiveRun{
				{Target: "codex", Operation: "upgrade"},
				{Target: "codex-cli", Operation: "upgrade"},
			},
		},
	}

	var out bytes.Buffer
	if err := writeStatusText(&out, snapshot); err != nil {
		t.Fatalf("writeStatusText: %v", err)
	}
	text := out.String()
	for _, fragment := range []string{
		"launch agent: loaded target=gui/501/io.goodkind.desktop-via-clyde.updater",
		"daemon rpc: responding socket=/tmp/daemon.sock",
		"active run: target=codex operation=upgrade",
		"active run: target=codex-cli operation=upgrade",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("text %q missing %q", text, fragment)
		}
	}
}

func TestWriteStatusJSONIncludesActiveRuns(t *testing.T) {
	snapshot := statusSnapshot{
		LaunchAgent: launchAgentStatus{Loaded: true, Target: "gui/501/io.goodkind.desktop-via-clyde.updater"},
		DaemonRPC:   daemonRPCStatus{Responding: true, Socket: "/tmp/daemon.sock"},
		Updater: &desktopviaclydev1.GetUpdaterStatusResponse{
			ActiveRuns: []*desktopviaclydev1.ActiveRun{
				{Target: "codex", Operation: "upgrade"},
				{Target: "codex-cli", Operation: "upgrade"},
			},
		},
	}

	var out bytes.Buffer
	if err := writeStatusJSON(context.Background(), &out, snapshot); err != nil {
		t.Fatalf("writeStatusJSON: %v", err)
	}
	body := out.String()
	for _, fragment := range []string{
		"\"launch_agent\"",
		"\"daemon_rpc\"",
		"\"active_runs\"",
		"\"target\": \"codex\"",
		"\"target\": \"codex-cli\"",
	} {
		if !strings.Contains(body, fragment) {
			t.Fatalf("json %q missing %q", body, fragment)
		}
	}
}
