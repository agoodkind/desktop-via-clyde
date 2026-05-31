package paths

import (
	"path/filepath"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestDerivedXDGPathsUseClydeRoots(t *testing.T) {
	stateHome := t.TempDir()
	cacheHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	t.Setenv("XDG_CACHE_HOME", cacheHome)

	wantStateRoot := filepath.Join(stateHome, "clyde")
	if got := StateRoot(); got != wantStateRoot {
		t.Fatalf("StateRoot() = %q, want %q", got, wantStateRoot)
	}
	if got := LogDir(); got != filepath.Join(wantStateRoot, "logs") {
		t.Fatalf("LogDir() = %q", got)
	}
	if got := ProcessLogPath(); got != filepath.Join(wantStateRoot, "logs", "desktop-via-clyde.jsonl") {
		t.Fatalf("ProcessLogPath() = %q", got)
	}
	if got := BackupRoot(); got != filepath.Join(wantStateRoot, "desktop-via-clyde-backup") {
		t.Fatalf("BackupRoot() = %q", got)
	}
	if got := StateFile(); got != filepath.Join(wantStateRoot, "desktop-via-clyde-state.json") {
		t.Fatalf("StateFile() = %q", got)
	}
	if got := StdioTeeLogDir(); got != filepath.Join(wantStateRoot, "logs", "stdio-tee") {
		t.Fatalf("StdioTeeLogDir() = %q", got)
	}
}

func TestBackupBundleUsesTargetIDAndAppName(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	target := targets.Target{ID: "codex", AppPath: "/Applications/Codex.app"}
	want := filepath.Join(StateRoot(), "desktop-via-clyde-backup", "codex", "Codex.app")
	if got := BackupBundle(target); got != want {
		t.Fatalf("BackupBundle() = %q, want %q", got, want)
	}
}
