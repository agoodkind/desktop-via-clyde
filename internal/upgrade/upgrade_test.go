package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/desktop-via-clyde/internal/testsupport"
)

const (
	anthropicRequirement = `identifier "com.anthropic.claudefordesktop" and anchor apple generic and certificate leaf[subject.OU] = Q6L2SF6YDW`
	goodkindRequirement  = `identifier "com.anthropic.claudefordesktop" and anchor apple generic and certificate leaf[subject.OU] = H3BMXM4W7H`
)

func TestMain(m *testing.M) {
	if err := testsupport.RegisterFixtureCapabilities(); err != nil {
		panic(err)
	}
	if err := RegisterBootstrapStrategies(); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
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
	if got.Signature == "" {
		t.Fatalf("Signature is empty")
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

func TestLoadOriginalDRUsesStateEntry(t *testing.T) {
	installFixture(t)
	t.Setenv("HOME", t.TempDir())
	tg := targets.Target{
		ID:       "claude",
		AppPath:  "/Applications/Claude.app",
		ExecName: "Claude",
	}
	multiState := state.MultiState{
		Targets: map[string]state.TargetState{
			"claude": {
				PatchedVersion:                "1.8089.1",
				PatchedAt:                     time.Unix(0, 0).UTC(),
				SignIdentity:                  paths.SignIdentity(),
				OriginalDesignatedRequirement: anthropicRequirement,
			},
		},
	}
	if err := state.Save(paths.StateFile(), multiState); err != nil {
		t.Fatalf("state.Save: %v", err)
	}
	got, err := loadOriginalDR(context.Background(), tg, false)
	if err != nil {
		t.Fatalf("loadOriginalDR: %v", err)
	}
	if got != anthropicRequirement {
		t.Fatalf("loadOriginalDR = %q, want %q", got, anthropicRequirement)
	}
}

func TestLoadOriginalDRBootstrapsCleanClaude(t *testing.T) {
	installFixture(t)
	t.Setenv("HOME", t.TempDir())
	tg := testClaudeTarget(t)
	restore := replaceReadDesignatedRequirement(func(_ context.Context, path string) (string, error) {
		if path != paths.MainBinaryPath(tg) {
			t.Fatalf("readDesignatedRequirement path = %q, want %q", path, paths.MainBinaryPath(tg))
		}
		return anthropicRequirement, nil
	})
	t.Cleanup(restore)
	got, err := loadOriginalDR(context.Background(), tg, false)
	if err != nil {
		t.Fatalf("loadOriginalDR: %v", err)
	}
	if got != anthropicRequirement {
		t.Fatalf("loadOriginalDR = %q, want %q", got, anthropicRequirement)
	}
}

func TestLoadOriginalDRRejectsMissingStateWithRealBinary(t *testing.T) {
	installFixture(t)
	t.Setenv("HOME", t.TempDir())
	tg := testClaudeTarget(t)
	if err := os.WriteFile(paths.RealBinaryPath(tg), []byte("patched"), 0o755); err != nil {
		t.Fatalf("WriteFile real binary: %v", err)
	}
	_, err := loadOriginalDR(context.Background(), tg, false)
	if err == nil {
		t.Fatal("expected missing state plus real binary error")
	}
	if !strings.Contains(err.Error(), "has no state entry") {
		t.Fatalf("error = %q, want missing state text", err.Error())
	}
}

func TestLoadOriginalDRRejectsLocalRequirementBootstrap(t *testing.T) {
	installFixture(t)
	t.Setenv("HOME", t.TempDir())
	tg := testClaudeTarget(t)
	restore := replaceReadDesignatedRequirement(func(context.Context, string) (string, error) {
		return goodkindRequirement, nil
	})
	t.Cleanup(restore)
	_, err := loadOriginalDR(context.Background(), tg, false)
	if err == nil {
		t.Fatal("expected local signing requirement error")
	}
	if !strings.Contains(err.Error(), paths.SignTeamID()) {
		t.Fatalf("error = %q, want local team id", err.Error())
	}
}

func TestVerifyDownloadSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "Codex.zip")
	data := []byte("zip bytes")
	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	signature := ed25519.Sign(privateKey, data)
	tg := targets.Target{
		ID: "codex",
		Updater: targets.Updater{
			SparklePublicKey: base64.StdEncoding.EncodeToString(publicKey),
		},
	}
	manifest := updateManifest{
		Signature: base64.StdEncoding.EncodeToString(signature),
	}
	ok, err := verifyDownloadSignature(patch.NewRunner(context.Background(), false, io.Discard), tg, manifest, zipPath, false)
	if err != nil {
		t.Fatalf("verifyDownloadSignature: %v", err)
	}
	if !ok {
		t.Fatalf("verifyDownloadSignature ok = false, want true")
	}
}

func testClaudeTarget(t *testing.T) targets.Target {
	t.Helper()
	appPath := filepath.Join(t.TempDir(), "Claude.app")
	macosDir := filepath.Join(appPath, "Contents", "MacOS")
	if err := os.MkdirAll(macosDir, 0o755); err != nil {
		t.Fatalf("MkdirAll MacOS: %v", err)
	}
	tg := targets.Target{
		ID:       "claude",
		AppPath:  appPath,
		ExecName: "Claude",
		Extensions: extensions.Target{
			OriginalDRBootstrapCapability: "clean-main-binary",
		},
	}
	if err := os.WriteFile(paths.MainBinaryPath(tg), []byte("clean"), 0o755); err != nil {
		t.Fatalf("WriteFile main binary: %v", err)
	}
	return tg
}

func replaceReadDesignatedRequirement(fn func(context.Context, string) (string, error)) func() {
	original := readDesignatedRequirement
	readDesignatedRequirement = fn
	return func() {
		readDesignatedRequirement = original
	}
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

func TestVerifySparkleSignatureAllowsExtractedBundleKeyRotation(t *testing.T) {
	oldPublicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey old: %v", err)
	}
	newPublicKey, newPrivateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey new: %v", err)
	}
	tmpDir := t.TempDir()
	zipPath := filepath.Join(tmpDir, "Codex.zip")
	data := []byte("zip bytes")
	if err := os.WriteFile(zipPath, data, 0o644); err != nil {
		t.Fatalf("WriteFile zip: %v", err)
	}
	signature := ed25519.Sign(newPrivateKey, data)
	tg := targets.Target{
		ID:      "codex",
		AppPath: filepath.Join(tmpDir, "Current.app"),
		Updater: targets.Updater{
			SparklePublicKey: base64.StdEncoding.EncodeToString(oldPublicKey),
		},
	}
	manifest := updateManifest{
		Signature: base64.StdEncoding.EncodeToString(signature),
	}
	verified, err := verifyDownloadSignature(patch.NewRunner(context.Background(), false, io.Discard), tg, manifest, zipPath, false)
	if err != nil {
		t.Fatalf("verifyDownloadSignature: %v", err)
	}
	if verified {
		t.Fatalf("verifyDownloadSignature verified old key, want false before extracted key")
	}

	extractedInfoDir := filepath.Join(tmpDir, "Extracted.app", "Contents")
	if err := os.MkdirAll(extractedInfoDir, 0o755); err != nil {
		t.Fatalf("MkdirAll extracted info: %v", err)
	}
	infoPlist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleVersion</key>
  <string>3044</string>
  <key>SUPublicEDKey</key>
  <string>` + base64.StdEncoding.EncodeToString(newPublicKey) + `</string>
</dict>
</plist>
`
	if err := os.WriteFile(filepath.Join(extractedInfoDir, "Info.plist"), []byte(infoPlist), 0o644); err != nil {
		t.Fatalf("WriteFile Info.plist: %v", err)
	}
	if err := verifyExtractedSparkleSignature(patch.NewRunner(context.Background(), false, io.Discard), manifest, zipPath, filepath.Join(tmpDir, "Extracted.app"), verified, false); err != nil {
		t.Fatalf("verifyExtractedSparkleSignature: %v", err)
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

func installFixture(t *testing.T) {
	t.Helper()
	if err := testsupport.RegisterFixtureCapabilities(); err != nil {
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
