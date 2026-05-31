package patch

import (
	"bytes"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/paths"
)

func TestTeamIDFromSignIdentity(t *testing.T) {
	installFixture(t)
	got, err := teamIDFromSignIdentity(paths.SignIdentity())
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
