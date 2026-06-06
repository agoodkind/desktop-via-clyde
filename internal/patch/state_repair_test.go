package patch

import (
	"context"
	"encoding/json"
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

func TestEnsureOriginalDesignatedRequirementRequiresStateEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	target := targets.Target{ID: "codex", AppPath: "/Applications/Codex.app", ExecName: "Codex"}
	_, err := OriginalDesignatedRequirement(context.Background(), target)
	if err == nil {
		t.Fatal("expected missing state entry error")
	}
	if !strings.Contains(err.Error(), "run `desktop-via-clyde patch codex` first") {
		t.Fatalf("error = %q, want patch hint", err.Error())
	}
}

func TestEnsureOriginalDesignatedRequirementRequiresRecordedField(t *testing.T) {
	installFixture(t)
	t.Setenv("HOME", t.TempDir())
	target := targets.Target{ID: "codex", AppPath: "/Applications/Codex.app", ExecName: "Codex"}
	multiState := state.MultiState{
		Targets: map[string]state.TargetState{
			"codex": {
				PatchedVersion: "2620",
				PatchedAt:      time.Unix(0, 0).UTC(),
				SignIdentity:   paths.SignIdentity(),
			},
		},
	}
	if err := state.Update(paths.StateFile(), func(_ state.MultiState) (state.MultiState, error) {
		return multiState, nil
	}); err != nil {
		t.Fatalf("state.Update: %v", err)
	}
	_, err := OriginalDesignatedRequirement(context.Background(), target)
	if err == nil {
		t.Fatal("expected missing original designated requirement error")
	}
	if !strings.Contains(err.Error(), "has no recorded clean upstream DesignatedRequirement") {
		t.Fatalf("error = %q, want missing recorded requirement", err.Error())
	}
}

func TestResolveOriginalDRForPatchRejectsRealBinaryWithoutCleanState(t *testing.T) {
	installFixture(t)
	stateHome := t.TempDir()
	t.Setenv("XDG_STATE_HOME", stateHome)
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

	_, err := resolveOriginalDRForPatch(context.Background(), NewRunner(context.Background(), false, io.Discard), target)
	if err == nil {
		t.Fatal("expected missing clean upstream DR error")
	}
	if !strings.Contains(err.Error(), "state lacks a clean upstream DesignatedRequirement") {
		t.Fatalf("error = %q, want clean upstream DR failure", err.Error())
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
