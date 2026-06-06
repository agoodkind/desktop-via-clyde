package patch

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestResolveOriginalDRForPatchRecoversFromRealBinaryWithoutState(t *testing.T) {
	installFixture(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	target := targets.Target{
		ID:       "codex",
		AppPath:  filepath.Join(t.TempDir(), "Codex.app"),
		ExecName: "Codex",
	}
	if err := os.MkdirAll(filepath.Dir(paths.RealBinaryPath(target)), 0o755); err != nil {
		t.Fatalf("MkdirAll real binary parent: %v", err)
	}
	if err := os.WriteFile(paths.RealBinaryPath(target), []byte("patched"), 0o755); err != nil {
		t.Fatalf("WriteFile real binary: %v", err)
	}

	restore := readDesignatedRequirement
	readDesignatedRequirement = func(_ context.Context, path string) (string, error) {
		if path != paths.RealBinaryPath(target) {
			t.Fatalf("readDesignatedRequirement path = %q, want %q", path, paths.RealBinaryPath(target))
		}
		return `identifier "com.openai.chatgpt" and certificate leaf[subject.OU] = 2DC432GLL2`, nil
	}
	t.Cleanup(func() {
		readDesignatedRequirement = restore
	})

	got, err := resolveOriginalDRForPatch(context.Background(), NewRunner(context.Background(), false, io.Discard), target)
	if err != nil {
		t.Fatalf("resolveOriginalDRForPatch: %v", err)
	}
	if !strings.Contains(got, "2DC432GLL2") {
		t.Fatalf("resolveOriginalDRForPatch = %q, want upstream requirement recovered from .real", got)
	}
}

func TestResolveOriginalDRForPatchEmptyWhenCaptureFails(t *testing.T) {
	installFixture(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	target := targets.Target{
		ID:       "codex",
		AppPath:  filepath.Join(t.TempDir(), "Codex.app"),
		ExecName: "Codex",
	}

	restore := readDesignatedRequirement
	readDesignatedRequirement = func(context.Context, string) (string, error) {
		return "", errors.New("codesign unavailable")
	}
	t.Cleanup(func() {
		readDesignatedRequirement = restore
	})

	got, err := resolveOriginalDRForPatch(context.Background(), NewRunner(context.Background(), false, io.Discard), target)
	if err != nil {
		t.Fatalf("resolveOriginalDRForPatch returned error, want best-effort empty: %v", err)
	}
	if got != "" {
		t.Fatalf("resolveOriginalDRForPatch = %q, want empty when capture fails", got)
	}
}

func TestResolveOriginalDRForPatchRecapturesCleanMainBinaryWhenStoredDRIsLocal(t *testing.T) {
	installFixture(t)
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
	target := targets.Target{
		ID:       "codex",
		AppPath:  filepath.Join(t.TempDir(), "Codex.app"),
		ExecName: "Codex",
	}
	if err := os.MkdirAll(filepath.Dir(paths.MainBinaryPath(target)), 0o755); err != nil {
		t.Fatalf("MkdirAll main binary parent: %v", err)
	}
	if err := os.WriteFile(paths.MainBinaryPath(target), []byte("clean"), 0o755); err != nil {
		t.Fatalf("WriteFile main binary: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.StateFile()), 0o755); err != nil {
		t.Fatalf("MkdirAll state dir: %v", err)
	}
	data, err := json.Marshal(state.MultiState{
		Targets: map[string]state.TargetState{
			"codex": {
				PatchedVersion:                "2620",
				PatchedAt:                     time.Unix(0, 0).UTC(),
				SignIdentity:                  paths.SignIdentity(),
				OriginalDesignatedRequirement: `identifier "com.openai.chatgpt" and certificate leaf[subject.OU] = H3BMXM4W7H`,
			},
		},
	})
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if err := os.WriteFile(paths.StateFile(), data, 0o600); err != nil {
		t.Fatalf("WriteFile state: %v", err)
	}

	restore := readDesignatedRequirement
	readDesignatedRequirement = func(_ context.Context, path string) (string, error) {
		if path != paths.MainBinaryPath(target) {
			t.Fatalf("readDesignatedRequirement path = %q, want %q", path, paths.MainBinaryPath(target))
		}
		return `identifier "com.openai.chatgpt" and certificate leaf[subject.OU] = 2DC432GLL2`, nil
	}
	t.Cleanup(func() {
		readDesignatedRequirement = restore
	})

	got, err := resolveOriginalDRForPatch(context.Background(), NewRunner(context.Background(), false, io.Discard), target)
	if err != nil {
		t.Fatalf("resolveOriginalDRForPatch: %v", err)
	}
	if strings.Contains(got, paths.SignTeamID()) {
		t.Fatalf("resolveOriginalDRForPatch = %q, want upstream requirement", got)
	}
}
