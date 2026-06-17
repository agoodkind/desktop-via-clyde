package computeruseext

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestTeamIDFromSignIdentity(t *testing.T) {
	got, err := teamIDFromSignIdentity("Developer ID Application: Alex Goodkind (H3BMXM4W7H)")
	if err != nil {
		t.Fatalf("teamIDFromSignIdentity: %v", err)
	}
	if got != "H3BMXM4W7H" {
		t.Fatalf("teamIDFromSignIdentity = %q, want H3BMXM4W7H", got)
	}
}

func TestReplaceStandaloneTeamIDPreservesAppGroupPrefix(t *testing.T) {
	input := []byte("2DC432GLL2\x00prefix 2DC432GLL2.com.openai.sky.CUAService\n2DC432GLL2 ")
	out, replacements, alreadyPatched, err := replaceStandaloneTeamID(input, "2DC432GLL2", "H3BMXM4W7H")
	if err != nil {
		t.Fatalf("replaceStandaloneTeamID: %v", err)
	}
	if replacements != 2 {
		t.Fatalf("replacements = %d, want 2", replacements)
	}
	if alreadyPatched {
		t.Fatal("alreadyPatched = true, want false")
	}
	if !bytes.Contains(out, []byte("2DC432GLL2.com.openai.sky.CUAService")) {
		t.Fatalf("expected app group prefix to remain unchanged; got %q", string(out))
	}
	if got := countStandaloneToken(out, "2DC432GLL2"); got != 0 {
		t.Fatalf("standalone upstream team count = %d, want 0", got)
	}
	if got := countStandaloneToken(out, "H3BMXM4W7H"); got != 2 {
		t.Fatalf("standalone local team count = %d, want 2", got)
	}
}

func TestReplaceStandaloneTeamIDIdempotent(t *testing.T) {
	input := []byte("H3BMXM4W7H\x00prefix 2DC432GLL2.com.openai.sky.CUAService")
	out, replacements, alreadyPatched, err := replaceStandaloneTeamID(input, "2DC432GLL2", "H3BMXM4W7H")
	if err != nil {
		t.Fatalf("replaceStandaloneTeamID: %v", err)
	}
	if replacements != 0 {
		t.Fatalf("replacements = %d, want 0", replacements)
	}
	if !alreadyPatched {
		t.Fatal("alreadyPatched = false, want true")
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("idempotent replacement changed input: got %q want %q", string(out), string(input))
	}
}

func TestReplaceTeamRequirementPlist(t *testing.T) {
	input := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
<key>team-identifier</key>
<string>2DC432GLL2</string>
</dict>
</plist>`)
	out, changed, alreadyPatched, err := replaceTeamRequirementPlist(input, "2DC432GLL2", "H3BMXM4W7H")
	if err != nil {
		t.Fatalf("replaceTeamRequirementPlist: %v", err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if alreadyPatched {
		t.Fatal("alreadyPatched = true, want false")
	}
	got, err := teamRequirementPlistTeamID(out)
	if err != nil {
		t.Fatalf("teamRequirementPlistTeamID: %v", err)
	}
	if got != "H3BMXM4W7H" {
		t.Fatalf("team-identifier = %q, want H3BMXM4W7H", got)
	}
}

func TestReplaceTeamRequirementPlistIdempotent(t *testing.T) {
	input := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
<key>team-identifier</key>
<string>H3BMXM4W7H</string>
</dict>
</plist>`)
	out, changed, alreadyPatched, err := replaceTeamRequirementPlist(input, "2DC432GLL2", "H3BMXM4W7H")
	if err != nil {
		t.Fatalf("replaceTeamRequirementPlist: %v", err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
	if !alreadyPatched {
		t.Fatal("alreadyPatched = false, want true")
	}
	if !bytes.Equal(out, input) {
		t.Fatalf("idempotent replacement changed input: got %q want %q", string(out), string(input))
	}
}

func TestReplaceStandaloneTeamIDRejectsInvalidTeam(t *testing.T) {
	_, _, _, err := replaceStandaloneTeamID([]byte("2DC432GLL2"), "2DC432GLL2", "TOO-SHORT")
	if err == nil {
		t.Fatal("expected invalid team error")
	}
}

func TestBundledAuthPluginSourcePathUsesDeclaredSignTarget(t *testing.T) {
	policy := targets.ComputerUsePolicy{
		BundledAppPath: "Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app",
		AuthPluginPath: "/Library/Security/SecurityAgentPlugins/CodexComputerUseAuthorizationPlugin.bundle",
		SignTargets: []targets.ComputerUseSignTarget{
			{Path: "Contents/SharedSupport/Codex Computer Use Installer.app/Contents/Resources/CodexComputerUseAuthorizationPlugin.bundle"},
		},
	}
	got, ok := bundledAuthPluginSourcePath("/Applications/Codex.app", policy)
	if !ok {
		t.Fatal("bundledAuthPluginSourcePath ok = false, want true")
	}
	want := filepath.Join(
		"/Applications/Codex.app",
		"Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app",
		"Contents/SharedSupport/Codex Computer Use Installer.app/Contents/Resources/CodexComputerUseAuthorizationPlugin.bundle",
	)
	if got != want {
		t.Fatalf("bundledAuthPluginSourcePath = %q, want %q", got, want)
	}
}

func TestWriteExistingFileOpenErrorIncludesRewriteEvidence(t *testing.T) {
	dirPath := t.TempDir()
	err := writeExistingFile(dirPath, 0o755, []byte("data"))
	if err == nil {
		t.Fatal("writeExistingFile(dir) unexpectedly succeeded")
	}

	for _, fragment := range []string{
		"attempted operation=atomic replace existing file",
		"replace " + dirPath,
		"path=" + dirPath,
		"owner=",
		"mode=",
		"flags=",
		"xattrs=",
	} {
		if !strings.Contains(err.Error(), fragment) {
			t.Fatalf("error %q missing %q", err, fragment)
		}
	}
}

func TestWriteExistingFileReplacesContentsAndMode(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(filePath, []byte("old"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if err := writeExistingFile(filePath, 0o755, []byte("new-data")); err != nil {
		t.Fatalf("writeExistingFile: %v", err)
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "new-data" {
		t.Fatalf("contents = %q, want new-data", string(data))
	}
	info, err := os.Stat(filePath)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %#o, want 0755", info.Mode().Perm())
	}
}

func TestListPathXattrsReturnsSortedNames(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "helper")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := unix.Setxattr(filePath, "com.goodkind.test.second", []byte("2"), 0); err != nil {
		t.Skipf("Setxattr second: %v", err)
	}
	if err := unix.Setxattr(filePath, "com.goodkind.test.first", []byte("1"), 0); err != nil {
		t.Skipf("Setxattr first: %v", err)
	}

	xattrs, err := ReadPathXattrs(filePath)
	if err != nil {
		t.Fatalf("ReadPathXattrs: %v", err)
	}
	firstIndex := -1
	secondIndex := -1
	for index, xattr := range xattrs {
		if xattr == "com.goodkind.test.first" {
			firstIndex = index
		}
		if xattr == "com.goodkind.test.second" {
			secondIndex = index
		}
	}
	if firstIndex < 0 || secondIndex < 0 {
		t.Fatalf("xattrs = %v, want both custom xattrs present", xattrs)
	}
	if firstIndex > secondIndex {
		t.Fatalf("xattrs = %v, want custom xattrs sorted", xattrs)
	}
}
