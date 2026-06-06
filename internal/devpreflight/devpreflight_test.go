package devpreflight

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

// withStubs swaps the package seams for the duration of one test so the preflight
// never touches ~/Desktop or contacts App Store Connect, then restores them.
func withStubs(t *testing.T, credsErr error, generate func(context.Context, targets.DevelopmentSigningPolicy) (string, error)) {
	t.Helper()
	originalCreds := CredentialsDiscoverer
	originalGen := AssetGenerator
	CredentialsDiscoverer = func() error { return credsErr }
	AssetGenerator = generate
	t.Cleanup(func() {
		CredentialsDiscoverer = originalCreds
		AssetGenerator = originalGen
	})
}

func enabledTarget() targets.Target {
	return targets.Target{
		ID: "codex",
		DevelopmentSigning: &targets.DevelopmentSigningPolicy{
			Enabled: true,
		},
	}
}

func TestWarnSkipsWhenNotEnabled(t *testing.T) {
	withStubs(t, nil, func(context.Context, targets.DevelopmentSigningPolicy) (string, error) {
		t.Fatalf("generator must not be called when development signing is disabled")
		return "", nil
	})
	var out bytes.Buffer
	Warn(context.Background(), &out, false, targets.Target{ID: "codex"})
	if out.Len() != 0 {
		t.Fatalf("expected no output for a target without development signing, got %q", out.String())
	}
}

func TestWarnSilentWhenAssetsPresent(t *testing.T) {
	dir := t.TempDir()
	profile := writeFile(t, dir, "dev.provisionprofile")
	p12 := writeFile(t, dir, "dev.p12")
	password := writeFile(t, dir, "p12-password")
	withStubs(t, nil, func(context.Context, targets.DevelopmentSigningPolicy) (string, error) {
		t.Fatalf("generator must not run when no assets are missing")
		return "", nil
	})
	tg := enabledTarget()
	tg.DevelopmentSigning.ProfilePath = profile
	tg.DevelopmentSigning.P12Path = p12
	tg.DevelopmentSigning.P12PasswordFile = password

	var out bytes.Buffer
	Warn(context.Background(), &out, false, tg)
	if out.Len() != 0 {
		t.Fatalf("expected no output when every asset is present, got %q", out.String())
	}
}

func TestWarnCredentialsMissingNamesFiles(t *testing.T) {
	withStubs(t, errors.New("no key on disk"), func(context.Context, targets.DevelopmentSigningPolicy) (string, error) {
		t.Fatalf("generator must not run without credentials")
		return "", nil
	})
	var out bytes.Buffer
	Warn(context.Background(), &out, false, enabledTarget())
	text := out.String()
	for _, want := range []string{"WARNING", "AuthKey_<KEY_ID>.p8", "README.md", "-34018", "Continuing."} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in credentials-missing warning: %q", want, text)
		}
	}
}

func TestWarnCredentialsPresentSuggestsAutoGenerate(t *testing.T) {
	withStubs(t, nil, func(context.Context, targets.DevelopmentSigningPolicy) (string, error) {
		t.Fatalf("generator must not run unless auto_generate is set")
		return "", nil
	})
	var out bytes.Buffer
	Warn(context.Background(), &out, false, enabledTarget())
	if !strings.Contains(out.String(), "auto_generate=true") {
		t.Fatalf("expected the auto_generate suggestion, got %q", out.String())
	}
}

func TestWarnAutoGenerateRunsGenerator(t *testing.T) {
	called := false
	withStubs(t, nil, func(_ context.Context, _ targets.DevelopmentSigningPolicy) (string, error) {
		called = true
		return "/tmp/generated.provisionprofile", nil
	})
	tg := enabledTarget()
	tg.DevelopmentSigning.AutoGenerate = true

	var out bytes.Buffer
	Warn(context.Background(), &out, false, tg)
	if !called {
		t.Fatalf("expected the generator to run when auto_generate is set and credentials are present")
	}
	if !strings.Contains(out.String(), "generated development-signing assets") {
		t.Fatalf("expected a generation-success note, got %q", out.String())
	}
}

func TestWarnAutoGenerateSkippedOnDryRun(t *testing.T) {
	withStubs(t, nil, func(context.Context, targets.DevelopmentSigningPolicy) (string, error) {
		t.Fatalf("generator must not run during a dry run")
		return "", nil
	})
	tg := enabledTarget()
	tg.DevelopmentSigning.AutoGenerate = true

	var out bytes.Buffer
	Warn(context.Background(), &out, true, tg)
	if !strings.Contains(out.String(), "auto_generate=true") {
		t.Fatalf("expected the dry run to suggest auto_generate without generating, got %q", out.String())
	}
}

func TestWarnAutoGenerateFailureIsNonBlocking(t *testing.T) {
	withStubs(t, nil, func(context.Context, targets.DevelopmentSigningPolicy) (string, error) {
		return "", errors.New("App Store Connect rejected the request")
	})
	tg := enabledTarget()
	tg.DevelopmentSigning.AutoGenerate = true

	var out bytes.Buffer
	Warn(context.Background(), &out, false, tg)
	text := out.String()
	if !strings.Contains(text, "generation failed") {
		t.Fatalf("expected a non-blocking failure note, got %q", text)
	}
	if !strings.Contains(text, "Continuing.") {
		t.Fatalf("expected the failure note to signal it continues, got %q", text)
	}
}

func writeFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("asset"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
