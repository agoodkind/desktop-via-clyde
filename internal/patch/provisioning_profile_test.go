package patch

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestStepEmbedProvisioningProfileReplacesEmbeddedProfile(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "App.app")
	embedded := filepath.Join(appPath, "Contents", "embedded.provisionprofile")
	if err := os.MkdirAll(filepath.Dir(embedded), 0o755); err != nil {
		t.Fatalf("mkdir Contents: %v", err)
	}
	if err := os.WriteFile(embedded, []byte("STALE-UPSTREAM-PROFILE"), 0o644); err != nil {
		t.Fatalf("write stale profile: %v", err)
	}
	profilePath := filepath.Join(t.TempDir(), "local-team.provisionprofile")
	want := []byte("LOCAL-TEAM-PROFILE")
	if err := os.WriteFile(profilePath, want, 0o600); err != nil {
		t.Fatalf("write local profile: %v", err)
	}

	runner := NewRunner(context.Background(), false, io.Discard)
	target := targets.Target{ID: "fake", AppPath: appPath, ProvisioningProfile: profilePath}
	if err := stepEmbedProvisioningProfile(context.Background(), runner, target); err != nil {
		t.Fatalf("stepEmbedProvisioningProfile: %v", err)
	}
	got, err := os.ReadFile(embedded)
	if err != nil {
		t.Fatalf("read embedded profile: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("embedded profile = %q, want %q", got, want)
	}
}

func TestStepEmbedProvisioningProfileSkipsWhenUnset(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), "App.app")
	if err := os.MkdirAll(filepath.Join(appPath, "Contents"), 0o755); err != nil {
		t.Fatalf("mkdir Contents: %v", err)
	}
	runner := NewRunner(context.Background(), false, io.Discard)
	if err := stepEmbedProvisioningProfile(context.Background(), runner, targets.Target{ID: "fake", AppPath: appPath}); err != nil {
		t.Fatalf("stepEmbedProvisioningProfile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(appPath, "Contents", "embedded.provisionprofile")); !os.IsNotExist(err) {
		t.Fatalf("expected no embedded profile to be written when unset, stat err=%v", err)
	}
}
