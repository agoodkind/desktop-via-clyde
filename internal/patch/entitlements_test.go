package patch

import (
	"strings"
	"testing"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

func TestRewriteTeamScopedEntitlementsConvertsWildcardToExplicit(t *testing.T) {
	xml := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>` +
		`<key>com.apple.application-identifier</key><string>2DC432GLL2.com.openai.codex.beta</string>` +
		`<key>com.apple.developer.team-identifier</key><string>2DC432GLL2</string>` +
		`<key>keychain-access-groups</key><array><string>2DC432GLL2.*</string><string>2DC432GLL2.com.openai.shared</string></array>` +
		`</dict></plist>`
	got := rewriteTeamScopedEntitlements(xml, "H3BMXM4W7H")
	for _, want := range []string{
		"<string>H3BMXM4W7H.com.openai.codex.beta</string>",
		"<string>H3BMXM4W7H</string>",
		"<string>H3BMXM4W7H.com.openai.shared</string>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewrite missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "2DC432GLL2") {
		t.Fatalf("upstream team should be absent after rewrite:\n%s", got)
	}
	if strings.Contains(got, ".*</string>") {
		t.Fatalf("wildcard keychain group should be explicit:\n%s", got)
	}
}

func TestRewriteTeamScopedEntitlementsNoopWithoutLocalTeam(t *testing.T) {
	xml := `<key>com.apple.application-identifier</key><string>2DC432GLL2.com.openai.codex.beta</string>`
	if got := rewriteTeamScopedEntitlements(xml, ""); got != xml {
		t.Fatalf("expected no-op without local team, got:\n%s", got)
	}
}

func TestRewriteTeamScopedEntitlementsConvertsAlreadyLocalWildcard(t *testing.T) {
	xml := `<key>com.apple.application-identifier</key><string>H3BMXM4W7H.com.openai.codex.beta</string>` +
		`<key>keychain-access-groups</key><array><string>H3BMXM4W7H.*</string></array>`
	got := rewriteTeamScopedEntitlements(xml, "H3BMXM4W7H")
	if strings.Contains(got, ".*</string>") {
		t.Fatalf("already-local wildcard should be converted to explicit:\n%s", got)
	}
	if !strings.Contains(got, "<string>H3BMXM4W7H.com.openai.codex.beta</string>") {
		t.Fatalf("expected explicit app-id group:\n%s", got)
	}
}

var disableLibraryValidationPolicy = targets.EntitlementsPolicy{
	RequiredBooleanEntitlements: []string{
		"com.apple.security.cs.disable-library-validation",
	},
}

const sampleEntitlements = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>com.apple.security.cs.allow-jit</key>
	<true/>
	<key>com.apple.security.network.client</key>
	<true/>
</dict>
</plist>
`

func TestAugmentEntitlementsAddsDisableLibraryValidation(t *testing.T) {
	out, err := augmentEntitlements([]byte(sampleEntitlements), disableLibraryValidationPolicy)
	if err != nil {
		t.Fatalf("augmentEntitlements: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, "com.apple.security.cs.disable-library-validation") {
		t.Fatalf("expected disable-library-validation to be inserted; got:\n%s", s)
	}
	if !strings.Contains(s, "com.apple.security.cs.allow-jit") {
		t.Fatalf("expected existing entitlements to be preserved")
	}
}

func TestAugmentEntitlementsIdempotent(t *testing.T) {
	once, err := augmentEntitlements([]byte(sampleEntitlements), disableLibraryValidationPolicy)
	if err != nil {
		t.Fatalf("first augment: %v", err)
	}
	twice, err := augmentEntitlements(once, disableLibraryValidationPolicy)
	if err != nil {
		t.Fatalf("second augment: %v", err)
	}
	if string(once) != string(twice) {
		t.Fatalf("augmentEntitlements not idempotent:\nonce=%s\ntwice=%s", once, twice)
	}
}

func TestStripEntitlementKey(t *testing.T) {
	out, err := stripEntitlementKey(sampleEntitlements, "com.apple.security.network.client")
	if err != nil {
		t.Fatalf("stripEntitlementKey: %v", err)
	}
	if strings.Contains(out, "com.apple.security.network.client") {
		t.Fatalf("expected stripped key to be gone:\n%s", out)
	}
	if !strings.Contains(out, "com.apple.security.cs.allow-jit") {
		t.Fatalf("expected other key to remain")
	}
}

// codesign emits entitlements as one line with no whitespace between tags.
// This is the actual shape on disk for /Applications/Codex.app, and the
// regex strip must handle it correctly.
const compactEntitlements = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "https://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>com.apple.application-identifier</key><string>2DC432GLL2.com.openai.codex</string><key>com.apple.developer.team-identifier</key><string>2DC432GLL2</string><key>com.apple.security.app-sandbox</key><false/><key>com.apple.security.automation.apple-events</key><true/><key>com.apple.security.cs.allow-jit</key><true/><key>keychain-access-groups</key><array><string>2DC432GLL2.*</string></array></dict></plist>`

const claudeEntitlements = `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "https://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>com.apple.application-identifier</key><string>Q6L2SF6YDW.com.anthropic.claudefordesktop</string><key>com.apple.developer.team-identifier</key><string>Q6L2SF6YDW</string><key>com.apple.security.cs.allow-jit</key><true/><key>com.apple.security.device.audio-input</key><true/><key>com.apple.security.device.bluetooth</key><true/><key>keychain-access-groups</key><array><string>Q6L2SF6YDW.com.anthropic.claude.webauthn</string></array></dict></plist>`

func TestStripEntitlementKeyCompactXML(t *testing.T) {
	keys := []string{
		"com.apple.application-identifier",
		"com.apple.developer.team-identifier",
		"keychain-access-groups",
	}
	out := compactEntitlements
	for _, k := range keys {
		stripped, err := stripEntitlementKey(out, k)
		if err != nil {
			t.Fatalf("stripEntitlementKey(%s): %v", k, err)
		}
		out = stripped
	}
	for _, k := range keys {
		if strings.Contains(out, k) {
			t.Fatalf("expected %s to be stripped from compact xml; got:\n%s", k, out)
		}
	}
	for _, k := range []string{"com.apple.security.app-sandbox", "com.apple.security.cs.allow-jit"} {
		if !strings.Contains(out, k) {
			t.Fatalf("expected %s to remain; got:\n%s", k, out)
		}
	}
	final, err := augmentEntitlements([]byte(out), disableLibraryValidationPolicy)
	if err != nil {
		t.Fatalf("augmentEntitlements after strip: %v", err)
	}
	if !strings.Contains(string(final), "com.apple.security.cs.disable-library-validation") {
		t.Fatalf("expected disable-library-validation after augment; got:\n%s", string(final))
	}
}

func TestAugmentEntitlementsAppliesCodexPolicy(t *testing.T) {
	policy := targets.EntitlementsPolicy{
		Strip: []string{
			"com.apple.application-identifier",
			"com.apple.developer.team-identifier",
			"keychain-access-groups",
		},
		RequiredBooleanEntitlements: []string{
			"com.apple.security.automation.apple-events",
			"com.apple.security.cs.disable-library-validation",
		},
	}
	out, err := augmentEntitlements([]byte(compactEntitlements), policy)
	if err != nil {
		t.Fatalf("augmentEntitlements: %v", err)
	}
	result := string(out)
	for _, key := range policy.Strip {
		if strings.Contains(result, key) {
			t.Fatalf("expected %s to be stripped; got:\n%s", key, result)
		}
	}
	for _, key := range policy.RequiredBooleanEntitlements {
		if !hasBooleanEntitlement(out, key) {
			t.Fatalf("expected %s to be present and true; got:\n%s", key, result)
		}
	}
}

func TestAugmentEntitlementsAppliesClaudePolicy(t *testing.T) {
	policy := targets.EntitlementsPolicy{
		Strip: []string{
			"com.apple.application-identifier",
			"com.apple.developer.team-identifier",
			"keychain-access-groups",
		},
		RequiredBooleanEntitlements: []string{
			"com.apple.security.cs.disable-library-validation",
		},
	}
	out, err := augmentEntitlements([]byte(claudeEntitlements), policy)
	if err != nil {
		t.Fatalf("augmentEntitlements: %v", err)
	}
	result := string(out)
	for _, key := range policy.Strip {
		if strings.Contains(result, key) {
			t.Fatalf("expected %s to be stripped; got:\n%s", key, result)
		}
	}
	for _, key := range []string{
		"com.apple.security.cs.allow-jit",
		"com.apple.security.device.audio-input",
		"com.apple.security.device.bluetooth",
	} {
		if !strings.Contains(result, key) {
			t.Fatalf("expected %s to remain; got:\n%s", key, result)
		}
	}
	if !hasBooleanEntitlement(out, "com.apple.security.cs.disable-library-validation") {
		t.Fatalf("expected disable-library-validation to be present and true; got:\n%s", result)
	}
}

func TestAugmentEntitlementsStripsComputerUseApplicationGroups(t *testing.T) {
	policy := targets.EntitlementsPolicy{
		Strip: []string{
			"com.apple.security.application-groups",
		},
		RequiredBooleanEntitlements: []string{
			"com.apple.security.automation.apple-events",
		},
	}
	input := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>com.apple.security.application-groups</key><array><string>2DC432GLL2.com.openai.sky.CUAService</string></array><key>com.apple.security.automation.apple-events</key><true/></dict></plist>`
	out, err := augmentEntitlements([]byte(input), policy)
	if err != nil {
		t.Fatalf("augmentEntitlements: %v", err)
	}
	if strings.Contains(string(out), "com.apple.security.application-groups") {
		t.Fatalf("expected application groups to be stripped; got:\n%s", string(out))
	}
	if !hasBooleanEntitlement(out, "com.apple.security.automation.apple-events") {
		t.Fatalf("expected Apple Events to remain true; got:\n%s", string(out))
	}
}

func TestAugmentEntitlementsAllowsEmptyComputerUseFallback(t *testing.T) {
	policy := targets.EntitlementsPolicy{
		RequiredBooleanEntitlements: []string{
			"com.apple.security.automation.apple-events",
		},
	}
	out, err := augmentEntitlements([]byte(emptyEntitlementsXML), policy)
	if err != nil {
		t.Fatalf("augmentEntitlements: %v", err)
	}
	if !hasBooleanEntitlement(out, "com.apple.security.automation.apple-events") {
		t.Fatalf("expected Apple Events to be inserted; got:\n%s", string(out))
	}
}

func TestEnsureBooleanEntitlementRejectsFalseValue(t *testing.T) {
	_, err := ensureBooleanEntitlement(compactEntitlements, "com.apple.security.app-sandbox")
	if err == nil {
		t.Fatal("expected error for existing non-true entitlement")
	}
}

func TestHasBooleanEntitlement(t *testing.T) {
	if !hasBooleanEntitlement([]byte(compactEntitlements), "com.apple.security.automation.apple-events") {
		t.Fatal("expected compact true entitlement to match")
	}
	if hasBooleanEntitlement([]byte(compactEntitlements), "com.apple.security.app-sandbox") {
		t.Fatal("expected false entitlement not to match")
	}
}
