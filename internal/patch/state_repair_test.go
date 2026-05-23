package patch

import (
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
	_, err := EnsureOriginalDesignatedRequirement(target)
	if err == nil {
		t.Fatal("expected missing state entry error")
	}
	if !strings.Contains(err.Error(), "run `desktop-via-clyde patch codex` first") {
		t.Fatalf("error = %q, want patch hint", err.Error())
	}
}

func TestEnsureOriginalDesignatedRequirementReportsMissingBackupRepair(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	target := targets.Target{ID: "codex", AppPath: "/Applications/Codex.app", ExecName: "Codex"}
	multiState := state.MultiState{
		Targets: map[string]state.TargetState{
			"codex": {
				PatchedVersion: "2620",
				PatchedAt:      time.Unix(0, 0).UTC(),
				SignIdentity:   paths.SignIdentity,
			},
		},
	}
	if err := state.Save(paths.StateFile(), multiState); err != nil {
		t.Fatalf("state.Save: %v", err)
	}
	_, err := EnsureOriginalDesignatedRequirement(target)
	if err == nil {
		t.Fatal("expected missing backup repair error")
	}
	if !strings.Contains(err.Error(), "repair from backup failed") {
		t.Fatalf("error = %q, want repair failure", err.Error())
	}
}
