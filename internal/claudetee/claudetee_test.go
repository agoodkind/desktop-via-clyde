package claudetee

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareVersionsNumericOrdering(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.1", -1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "1.99.99", 1},
		{"1.0", "1.0.0", -1},
		{"1.0.0", "1.0.0", 0},
		{"2.1.149", "2.1.150", -1},
		{"2.1.150", "2.1.149", 1},
	}
	for _, c := range cases {
		got := compareVersions(c.a, c.b)
		if got != c.want {
			t.Fatalf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestResolveBundledCLIPathPicksGreatestVersion(t *testing.T) {
	home := t.TempDir()
	appSupport := filepath.Join(home, AppSupportRel)
	// Three versions; the resolver must pick the greatest by version sort.
	for _, v := range []string{"2.1.99", "2.1.150", "2.1.149"} {
		dir := filepath.Join(appSupport, v, "claude.app", "Contents", "MacOS")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "claude"), []byte("fake"), 0o755); err != nil {
			t.Fatalf("write claude: %v", err)
		}
	}
	got, err := ResolveBundledCLIPath(Options{HomeDir: home})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(home, AppSupportRel, "2.1.150", BundledCLIRel)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveBundledCLIPathHonorsVersionDirOverride(t *testing.T) {
	home := t.TempDir()
	got, err := ResolveBundledCLIPath(Options{HomeDir: home, VersionDir: "9.9.9"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	want := filepath.Join(home, AppSupportRel, "9.9.9", BundledCLIRel)
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveBundledCLIPathHonorsBundledCLIPathOverride(t *testing.T) {
	override := "/some/explicit/path/claude"
	got, err := ResolveBundledCLIPath(Options{
		HomeDir:        "/tmp/should-be-ignored",
		VersionDir:     "should-be-ignored",
		BundledCLIPath: override,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != override {
		t.Fatalf("got %q, want %q", got, override)
	}
}

func TestInstallDryRunWritesNothing(t *testing.T) {
	home, bundled := setupFakeBundledCLI(t)
	originalBytes := mustRead(t, bundled)

	var out bytes.Buffer
	err := Install(context.Background(), Options{
		HomeDir: home,
		DryRun:  true,
		LogDir:  "/tmp/should-show-up",
		Out:     &out,
	})
	if err != nil {
		t.Fatalf("dry-run install: %v", err)
	}

	if got := mustRead(t, bundled); !bytes.Equal(got, originalBytes) {
		t.Fatalf("dry-run install mutated %s", bundled)
	}
	if _, err := os.Stat(bundled + ".real"); err == nil {
		t.Fatalf(".real sibling unexpectedly created by dry-run install")
	}
	for _, want := range []string{"dry-run:", "/tmp/should-show-up", bundled, bundled + ".real"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("dry-run output missing %q\n%s", want, out.String())
		}
	}
}

func TestInstallRefusesWhenRealExists(t *testing.T) {
	home, bundled := setupFakeBundledCLI(t)
	if err := os.WriteFile(bundled+".real", []byte("pre-existing"), 0o755); err != nil {
		t.Fatalf("seed .real: %v", err)
	}
	var out bytes.Buffer
	err := Install(context.Background(), Options{HomeDir: home, Out: &out})
	if err == nil {
		t.Fatalf("expected install to refuse when .real already exists")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error, got: %v", err)
	}
}

func TestUninstallRefusesWhenNoRealSibling(t *testing.T) {
	home, _ := setupFakeBundledCLI(t)
	var out bytes.Buffer
	err := Uninstall(context.Background(), Options{HomeDir: home, Out: &out})
	if err == nil {
		t.Fatalf("expected uninstall to refuse when there is no .real")
	}
	if !strings.Contains(err.Error(), "nothing to restore") {
		t.Fatalf("expected 'nothing to restore' error, got: %v", err)
	}
}

// setupFakeBundledCLI builds a fake claude-code/<version>/claude.app tree
// under a temp HOME and returns the home dir plus the bundled CLI path. The
// fake claude binary is just a few bytes; tests that need a working real
// binary use a shell script when needed.
func setupFakeBundledCLI(t *testing.T) (homeDir string, bundledCLIPath string) {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, AppSupportRel, "2.1.149", "claude.app", "Contents", "MacOS")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	bundled := filepath.Join(dir, "claude")
	if err := os.WriteFile(bundled, []byte("fake-original-claude"), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}
	return home, bundled
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}
