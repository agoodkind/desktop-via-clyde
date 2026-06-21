package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestMain(m *testing.M) {
	if err := registerFixtureCapabilities(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// recordingProgress captures the terminal outcome an operation declares so tests
// can assert it without a live renderer.
type recordingProgress struct {
	outcome    clioutput.Outcome
	outcomeSet bool
	steps      []string
	skips      []string
	failures   []string
}

func (p *recordingProgress) Step(detail string) {
	p.steps = append(p.steps, detail)
}

func (p *recordingProgress) Skip(detail string) {
	p.skips = append(p.skips, detail)
}

func (p *recordingProgress) Fail(detail string) {
	p.failures = append(p.failures, detail)
}

func (p *recordingProgress) SetOutcome(outcome clioutput.Outcome, _ string) {
	p.outcome = outcome
	p.outcomeSet = true
}

func TestParseHTTPPathJSONManifest(t *testing.T) {
	body := []byte(`{"url":"https://downloads.cursor.com/production/abc/darwin/arm64/Cursor-darwin-arm64.zip","name":"3.5.30"}`)
	got, err := parseHTTPPathJSONManifest(body)
	if err != nil {
		t.Fatalf("parseHTTPPathJSONManifest: %v", err)
	}
	if got.Name != "3.5.30" {
		t.Fatalf("Name = %q, want 3.5.30", got.Name)
	}
	if !strings.HasSuffix(got.URL, "Cursor-darwin-arm64.zip") {
		t.Fatalf("URL = %q, want Cursor zip", got.URL)
	}
}

func TestParseSparkleAppcast(t *testing.T) {
	body := []byte(`<?xml version='1.0' encoding='utf-8'?>
<rss xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle" version="2.0">
  <channel>
    <item>
      <title>26.519.41501</title>
      <sparkle:version>3044</sparkle:version>
      <sparkle:shortVersionString>26.519.41501</sparkle:shortVersionString>
      <sparkle:hardwareRequirements>arm64</sparkle:hardwareRequirements>
      <enclosure url="https://persistent.oaistatic.com/codex-app-prod/Codex-darwin-arm64-26.519.41501.zip" length="475627618" type="application/octet-stream" sparkle:edSignature="HUfS5pD969LVaWjAYyJqpnSzBsBYs8xJ9YHBLERMZ0cTdA3NLb5hmjZ63792NfpLO44LnWPwlbQFVpn31hNZAA==" />
      <sparkle:deltas>
        <enclosure url="https://persistent.oaistatic.com/codex-app-prod/Codex3044-2620-arm64.delta" sparkle:deltaFrom="2620" />
      </sparkle:deltas>
    </item>
  </channel>
</rss>`)
	got, err := parseSparkleAppcast(body)
	if err != nil {
		t.Fatalf("parseSparkleAppcast: %v", err)
	}
	if got.Name != "3044" {
		t.Fatalf("Name = %q, want 3044", got.Name)
	}
	if !strings.HasSuffix(got.URL, "Codex-darwin-arm64-26.519.41501.zip") {
		t.Fatalf("URL = %q, want full Codex zip", got.URL)
	}
}

func TestParseSquirrelJSONManifest(t *testing.T) {
	body := []byte(`{
  "currentRelease": "1.8555.2",
  "releases": [
    {
      "version": "1.8555.2",
      "updateTo": {
        "name": "Claude 1.8555.2",
        "version": "1.8555.2",
        "pub_date": "2026-05-22T23:55:31.590037",
        "url": "https://downloads.claude.ai/releases/darwin/universal/1.8555.2/Claude-a476c316c741715263e34f9c9d2bc45b6d0f21c7.zip",
        "notes": "Production Release - No Notes"
      }
    }
  ]
}`)
	got, err := parseSquirrelJSONManifest(body)
	if err != nil {
		t.Fatalf("parseSquirrelJSONManifest: %v", err)
	}
	if got.Name != "1.8555.2" {
		t.Fatalf("Name = %q, want 1.8555.2", got.Name)
	}
	if !strings.HasSuffix(got.URL, "Claude-a476c316c741715263e34f9c9d2bc45b6d0f21c7.zip") {
		t.Fatalf("URL = %q, want Claude zip", got.URL)
	}
}

func TestParseTauriMinisignManifest(t *testing.T) {
	body := []byte(`{
  "version": "1.2.3",
  "notes": "Test release",
  "pub_date": "2026-06-01T00:00:00Z",
  "url": "https://updates.example.invalid/download",
  "signature": "trusted-signature",
  "format": "app"
}`)
	got, err := parseTauriMinisignManifest(body)
	if err != nil {
		t.Fatalf("parseTauriMinisignManifest: %v", err)
	}
	if got.Name != "1.2.3" {
		t.Fatalf("Name = %q, want 1.2.3", got.Name)
	}
	if got.Signature != "trusted-signature" {
		t.Fatalf("Signature = %q, want trusted-signature", got.Signature)
	}
	if got.Format != "app" {
		t.Fatalf("Format = %q, want app", got.Format)
	}
}

func TestParseTauriMinisignManifestRejectsNonAppFormat(t *testing.T) {
	body := []byte(`{
  "version": "1.2.3",
  "url": "https://updates.example.invalid/download",
  "signature": "trusted-signature",
  "format": "dmg"
}`)
	_, err := parseTauriMinisignManifest(body)
	if err == nil {
		t.Fatal("parseTauriMinisignManifest should reject non-app format")
	}
	if !strings.Contains(err.Error(), "want app") {
		t.Fatalf("error = %q, want app format rejection", err)
	}
}

func TestArchiveNameUsesURLPathBase(t *testing.T) {
	got := archiveName(targets.Target{ID: "codex"}, "https://persistent.oaistatic.com/codex-app-prod/Codex-darwin-arm64-26.519.41501.zip?cache=1")
	if got != "Codex-darwin-arm64-26.519.41501.zip" {
		t.Fatalf("archiveName = %q", got)
	}
}

func TestArchiveNameFallback(t *testing.T) {
	got := archiveName(targets.Target{ID: "claude"}, "::::")
	if got != "claude.zip" {
		t.Fatalf("archiveName fallback = %q", got)
	}
}

func TestManifestArchiveNameUsesTauriTarGzName(t *testing.T) {
	tg := targets.Target{
		ID:       "conductor",
		AppPath:  "/Applications/Conductor.app",
		ExecName: "Conductor",
		Updater: targets.Updater{
			Kind: targets.UpdaterTauriMinisign,
		},
	}
	got := manifestArchiveName(tg, updateManifest{
		URL:       "https://updates.example.invalid/download",
		Name:      "1.2.3",
		Signature: "trusted-signature",
		Format:    "app",
	})
	if got != "Conductor.app.tar.gz" {
		t.Fatalf("manifestArchiveName = %q, want Conductor.app.tar.gz", got)
	}
	if !strings.HasSuffix(got, ".tar.gz") {
		t.Fatalf("manifestArchiveName = %q, want .tar.gz suffix", got)
	}
}

func TestManifestArchiveNameUsesTauriFallback(t *testing.T) {
	tg := targets.Target{
		ID: "conductor",
		Updater: targets.Updater{
			Kind: targets.UpdaterTauriMinisign,
		},
	}
	got := manifestArchiveName(tg, updateManifest{Name: "1.2.3"})
	if got != "update.app.tar.gz" {
		t.Fatalf("manifestArchiveName = %q, want update.app.tar.gz", got)
	}
}

func TestCursorUpdaterDefaultChannelIsDev(t *testing.T) {
	tg := lookupConfiguredTarget(t, "cursor")
	got, err := tg.Updater.ResolveChannel("")
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	if got != "dev" {
		t.Fatalf("default cursor channel = %q, want dev", got)
	}
}

func TestCodexUpdaterDefaultChannelIsBeta(t *testing.T) {
	tg := lookupConfiguredTarget(t, "codex")
	got, err := tg.Updater.ResolveChannel("")
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	if got != "beta" {
		t.Fatalf("default codex channel = %q, want beta", got)
	}
	url, err := tg.Updater.URLWithChannel(got)
	if err != nil {
		t.Fatalf("URLForChannel: %v", err)
	}
	if !strings.Contains(url, "codex-app-beta") {
		t.Fatalf("default codex channel URL = %q, want beta appcast", url)
	}
}

func TestClaudeUpdaterDoesNotRequireChannel(t *testing.T) {
	tg := lookupConfiguredTarget(t, "claude")
	got, err := tg.Updater.ResolveChannel("")
	if err != nil {
		t.Fatalf("ResolveChannel: %v", err)
	}
	if got != "" {
		t.Fatalf("default claude channel = %q, want empty", got)
	}
}

func TestRunTreatsHTTPPathNoUpdateAsSkippedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	appPath := writeBundleVersion(t, "Cursor.app", "3.7.2")
	tg := targets.Target{
		ID:      "cursor",
		AppPath: appPath,
		Updater: targets.Updater{
			Kind:           targets.UpdaterHTTPPathJSONManifest,
			URLTemplate:    server.URL + "/api/update/{version}/{channel}",
			UserAgent:      "desktop-via-clyde-test/{version}",
			DefaultChannel: "dev",
			Channels: []spec.UpdaterChannel{
				{Name: "dev"},
			},
		},
	}
	var out strings.Builder
	progress := &recordingProgress{}
	if err := Run(context.Background(), tg, Options{Out: &out, Progress: progress}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, want := range []string{"current version=3.7.2 channel=dev", "target=cursor no update available on dev channel; nothing to do"} {
		requireProgressStep(t, progress, want)
	}
	if !progress.outcomeSet || progress.outcome != clioutput.OutcomeSkipped {
		t.Fatalf("outcome = %q set=%v, want skipped", progress.outcome, progress.outcomeSet)
	}
}

func TestRunAlreadyOnLatestVersionReportsSkipped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"url":"https://example.invalid/Cursor.zip","name":"3.7.2"}`))
	}))
	t.Cleanup(server.Close)
	appPath := writeBundleVersion(t, "Cursor.app", "3.7.2")
	// A patched bundle keeps the original executable at <exec>.real.
	realPath := filepath.Join(appPath, "Contents", "MacOS", "Cursor.real")
	if err := os.WriteFile(realPath, []byte("clean"), 0o755); err != nil {
		t.Fatalf("WriteFile real binary: %v", err)
	}
	tg := targets.Target{
		ID:       "cursor",
		AppPath:  appPath,
		ExecName: "Cursor",
		Updater: targets.Updater{
			Kind:           targets.UpdaterHTTPPathJSONManifest,
			URLTemplate:    server.URL + "/api/update/{version}/{channel}",
			UserAgent:      "desktop-via-clyde-test/{version}",
			DefaultChannel: "dev",
			Channels: []spec.UpdaterChannel{
				{Name: "dev"},
			},
		},
	}
	var out strings.Builder
	progress := &recordingProgress{}
	if err := Run(context.Background(), tg, Options{Out: &out, Progress: progress}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	requireProgressStep(t, progress, "already on version 3.7.2; nothing to do")
	if !progress.outcomeSet || progress.outcome != clioutput.OutcomeSkipped {
		t.Fatalf("outcome = %q set=%v, want skipped", progress.outcome, progress.outcomeSet)
	}
}

func TestRunMissingBundleDryRunInstallsFromUpdater(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version='1.0' encoding='utf-8'?>
<rss xmlns:sparkle="http://www.andymatuschak.org/xml-namespaces/sparkle" version="2.0">
  <channel>
    <item>
      <title>26.519.41501</title>
      <sparkle:version>3044</sparkle:version>
      <sparkle:hardwareRequirements>arm64</sparkle:hardwareRequirements>
      <enclosure url="` + "https://example.invalid/Codex.zip" + `" length="1" type="application/octet-stream" />
    </item>
  </channel>
</rss>`))
	}))
	t.Cleanup(server.Close)
	tg := targets.Target{
		ID:       "codex",
		AppPath:  filepath.Join(t.TempDir(), "Codex.app"),
		BundleID: "com.openai.codex.beta",
		ExecName: "Codex",
		Entitlements: &targets.EntitlementsPolicy{
			Strip:                       nil,
			RequiredBooleanEntitlements: nil,
		},
		Updater: targets.Updater{
			Kind:      targets.UpdaterSparkleAppcast,
			URL:       server.URL + "/appcast.xml",
			UserAgent: "desktop-via-clyde-test",
		},
	}
	var out strings.Builder
	if err := Run(context.Background(), tg, Options{DryRun: true, Out: &out}); err != nil {
		t.Fatalf("Run missing bundle dry-run: %v\noutput:\n%s", err, out.String())
	}
	output := out.String()
	for _, want := range []string{
		"target=codex current version=0.0.0 updater=sparkle_appcast",
		"target=codex app missing",
		"target=codex manifest: name=3044",
		"target=codex patch complete",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, output)
		}
	}
}

func TestExtractZipFindsTargetAppRootInTempDir(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	appDir := filepath.Join(sourceDir, "Fake.app", "Contents", "MacOS")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	zipPath := filepath.Join(tmpDir, "Fake.zip")
	cmd := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", "Fake.app", zipPath)
	cmd.Dir = sourceDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ditto create fixture zip: %v output=%q", err, string(out))
	}
	staging := filepath.Join(tmpDir, "staging")
	tg := targets.Target{
		ID:       "fake",
		AppPath:  "/Applications/Fake.app",
		ExecName: "Fake",
	}
	got, err := extractZip(context.Background(), patch.NewRunner(context.Background(), false, io.Discard), zipPath, staging, tg, false)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	want := filepath.Join(staging, "extracted", "Fake.app")
	if got != want {
		t.Fatalf("extractZip path = %q, want %q", got, want)
	}
	if _, err := os.Stat(got); err != nil {
		t.Fatalf("extracted app missing: %v", err)
	}
}

func TestExtractZipAcceptsSingleDifferentlyNamedAppRoot(t *testing.T) {
	tmpDir := t.TempDir()
	sourceDir := filepath.Join(tmpDir, "source")
	appDir := filepath.Join(sourceDir, "Codex (Beta).app", "Contents", "MacOS")
	if err := os.MkdirAll(appDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	zipPath := filepath.Join(tmpDir, "CodexBeta.zip")
	cmd := exec.Command("/usr/bin/ditto", "-c", "-k", "--keepParent", "Codex (Beta).app", zipPath)
	cmd.Dir = sourceDir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ditto create beta fixture zip: %v output=%q", err, string(out))
	}
	staging := filepath.Join(tmpDir, "staging")
	tg := targets.Target{
		ID:       "codex",
		AppPath:  "/Applications/Codex.app",
		ExecName: "Codex (Beta)",
	}
	got, err := extractZip(context.Background(), patch.NewRunner(context.Background(), false, io.Discard), zipPath, staging, tg, false)
	if err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	want := filepath.Join(staging, "extracted", "Codex (Beta).app")
	if got != want {
		t.Fatalf("extractZip path = %q, want %q", got, want)
	}
}

func TestExtractTarGzExtractsAppTreeAndSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "Fake.app.tar.gz")
	writeTarGzFixture(t, tarGzPath, []tarGzEntry{
		{name: "Fake.app/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "Fake.app/Contents/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "Fake.app/Contents/MacOS/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "Fake.app/Contents/MacOS/Fake", typeFlag: tar.TypeReg, mode: 0o755, body: "hello"},
		{name: "Fake.app/Contents/Resources/", typeFlag: tar.TypeDir, mode: 0o755},
		{
			name:     "Fake.app/Contents/Resources/FakeLink",
			typeFlag: tar.TypeSymlink,
			mode:     0o755,
			linkname: "../MacOS/Fake",
		},
	})
	staging := filepath.Join(tmpDir, "staging")
	tg := targets.Target{
		ID:       "fake",
		AppPath:  "/Applications/Fake.app",
		ExecName: "Fake",
		Updater: targets.Updater{
			Kind: targets.UpdaterTauriMinisign,
		},
	}
	got, err := extractTarGz(context.Background(), patch.NewRunner(context.Background(), false, io.Discard), tarGzPath, staging, tg, false)
	if err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	want := filepath.Join(staging, "extracted", "Fake.app")
	if got != want {
		t.Fatalf("extractTarGz path = %q, want %q", got, want)
	}
	payload, err := os.ReadFile(filepath.Join(got, "Contents", "MacOS", "Fake"))
	if err != nil {
		t.Fatalf("ReadFile extracted payload: %v", err)
	}
	if string(payload) != "hello" {
		t.Fatalf("payload = %q, want hello", payload)
	}
	linkTarget, err := os.Readlink(filepath.Join(got, "Contents", "Resources", "FakeLink"))
	if err != nil {
		t.Fatalf("Readlink in-dir symlink: %v", err)
	}
	if linkTarget != "../MacOS/Fake" {
		t.Fatalf("symlink target = %q, want ../MacOS/Fake", linkTarget)
	}
}

func TestExtractTarGzRejectsEscapingSymlink(t *testing.T) {
	tmpDir := t.TempDir()
	tarGzPath := filepath.Join(tmpDir, "Fake.app.tar.gz")
	writeTarGzFixture(t, tarGzPath, []tarGzEntry{
		{name: "Fake.app/", typeFlag: tar.TypeDir, mode: 0o755},
		{name: "Fake.app/link", typeFlag: tar.TypeSymlink, mode: 0o755, linkname: "../../evil"},
	})
	staging := filepath.Join(tmpDir, "staging")
	outsidePath := filepath.Join(staging, "evil")
	tg := targets.Target{
		ID:       "fake",
		AppPath:  "/Applications/Fake.app",
		ExecName: "Fake",
		Updater: targets.Updater{
			Kind: targets.UpdaterTauriMinisign,
		},
	}
	_, err := extractTarGz(context.Background(), patch.NewRunner(context.Background(), false, io.Discard), tarGzPath, staging, tg, false)
	if err == nil {
		t.Fatal("extractTarGz should reject escaping symlink")
	}
	if !strings.Contains(err.Error(), "points outside extraction root") {
		t.Fatalf("error = %q, want escaping symlink rejection", err)
	}
	if _, err := os.Lstat(outsidePath); err == nil {
		t.Fatalf("outside path %s should not exist", outsidePath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("Lstat outside path: %v", err)
	}
}

type tarGzEntry struct {
	name     string
	typeFlag byte
	mode     int64
	body     string
	linkname string
}

func writeTarGzFixture(t *testing.T, path string, entries []tarGzEntry) {
	t.Helper()
	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		header := &tar.Header{
			Name:     entry.name,
			Typeflag: entry.typeFlag,
			Mode:     entry.mode,
			Linkname: entry.linkname,
		}
		if entry.typeFlag == tar.TypeReg {
			header.Size = int64(len(entry.body))
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader %s: %v", entry.name, err)
		}
		if entry.typeFlag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte(entry.body)); err != nil {
				t.Fatalf("Write payload %s: %v", entry.name, err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("Close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("Close gzip writer: %v", err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile tar.gz fixture: %v", err)
	}
}

func writeBundleVersion(t *testing.T, bundleName, version string) string {
	t.Helper()
	appPath := filepath.Join(t.TempDir(), bundleName)
	contentsDir := filepath.Join(appPath, "Contents")
	if err := os.MkdirAll(filepath.Join(contentsDir, "MacOS"), 0o755); err != nil {
		t.Fatalf("MkdirAll Contents: %v", err)
	}
	infoPlist := renderBundleInfoPlist(t, map[string]string{"Version": version})
	if err := os.WriteFile(filepath.Join(contentsDir, "Info.plist"), []byte(infoPlist), 0o644); err != nil {
		t.Fatalf("WriteFile Info.plist: %v", err)
	}
	return appPath
}

func requireProgressStep(t *testing.T, progress *recordingProgress, want string) {
	t.Helper()
	for _, step := range progress.steps {
		if strings.Contains(step, want) {
			return
		}
	}
	t.Fatalf("progress missing step %q\nsteps:\n%s", want, strings.Join(progress.steps, "\n"))
}

// renderBundleInfoPlist loads the bundle Info.plist template from testdata and
// substitutes the supplied values, keeping the plist XML out of the Go source.
func renderBundleInfoPlist(t *testing.T, data map[string]string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "bundle-info.plist.tmpl"))
	if err != nil {
		t.Fatalf("read bundle-info plist template: %v", err)
	}
	tmpl, err := template.New("bundle-info").Parse(string(raw))
	if err != nil {
		t.Fatalf("parse bundle-info plist template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute bundle-info plist template: %v", err)
	}
	return buf.String()
}

func lookupConfiguredTarget(t *testing.T, id string) targets.Target {
	t.Helper()
	installFixture(t)
	for _, target := range targets.All() {
		if target.ID == id {
			return target
		}
	}
	t.Fatalf("missing target %q", id)
	return targets.Target{}
}

func installFixture(t *testing.T) {
	t.Helper()
	if err := registerFixtureCapabilities(); err != nil {
		t.Fatalf("RegisterFixtureCapabilities(): %v", err)
	}
	cfg, err := config.LoadPath(filepath.Join("..", "testconfig", "testdata", "current-config.toml"))
	if err != nil {
		t.Fatalf("LoadPath(current-config.toml): %v", err)
	}
	config.SetCurrent(cfg)
	t.Cleanup(func() {
		config.SetCurrent(nil)
	})
}
