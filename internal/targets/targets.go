// Package targets defines the registry of macOS apps that desktop-via-clyde
// can patch. Each target carries the absolute bundle path, the canonical
// executable name (which becomes argv[0] after the shim execv's the .real
// binary), and the keychain services whose ACLs need to be re-granted after
// re-signing.
package targets

import "fmt"

// UpdaterKind identifies the upstream update protocol used by a target.
type UpdaterKind string

const (
	// UpdaterCursorManifest is Cursor's JSON manifest endpoint.
	UpdaterCursorManifest UpdaterKind = "cursor-manifest"
	// UpdaterSparkleAppcast is Sparkle's XML appcast format.
	UpdaterSparkleAppcast UpdaterKind = "sparkle-appcast"
	// UpdaterClaudeSquirrel is Claude's Squirrel-style JSON endpoint.
	UpdaterClaudeSquirrel UpdaterKind = "claude-squirrel"
)

// Updater describes the upstream updater endpoint for one target.
type Updater struct {
	Kind              UpdaterKind
	URL               string
	Platform          string
	Product           string
	SparklePublicKey  string
	DeviceIDParamName string
}

// EntitlementsPolicy describes how a target's extracted entitlements are
// normalized before re-signing. Strip removes upstream Team-bound claims that
// cannot remain valid after local re-signing. RequiredBooleanEntitlements lists
// boolean entitlements that must be present and true on the patched main
// executable.
type EntitlementsPolicy struct {
	Strip                       []string
	RequiredBooleanEntitlements []string
}

// ComputerUseSignTarget describes one app bundle inside Codex Computer Use
// that must be re-signed after local trust-policy repair.
type ComputerUseSignTarget struct {
	Path         string
	Entitlements *EntitlementsPolicy
}

// ComputerUsePolicy describes the Codex companion helper bundle whose native
// IPC trust policy must match the locally re-signed Codex app.
type ComputerUsePolicy struct {
	HostAppPath           string
	BundledAppPath        string
	AppPathFromHome       string
	CacheAppGlobsFromHome []string
	UpstreamTrustedTeamID string
	TeamPatchBinaries     []string
	TeamRequirementPlists []string
	SignTargets           []ComputerUseSignTarget
}

// Target describes one patchable bundle.
type Target struct {
	// ID is the short slug used in CLI args, state.json, and backup paths.
	ID string
	// AppPath is the absolute path to the .app bundle in /Applications.
	AppPath string
	// BundleID is CFBundleIdentifier; informational, used in status output.
	BundleID string
	// ExecName is CFBundleExecutable; also argv[0] after the shim execv.
	ExecName string
	// Entitlements declares how extracted entitlements are transformed and
	// which boolean entitlements must survive on the patched main executable.
	Entitlements *EntitlementsPolicy
	// KeychainServices lists generic-password service names whose ACLs need
	// to be re-granted to the patched binary so the user does not see a
	// macOS keychain prompt on first launch after patching.
	KeychainServices []string
	// NestedSignPaths lists app-relative code objects that must be re-signed
	// before the outer .app bundle is sealed.
	NestedSignPaths []string
	// PreservedNestedCodePaths lists app-relative code objects that must keep
	// their upstream signature and can be restored from the backup before the
	// outer .app bundle is sealed.
	PreservedNestedCodePaths []string
	// ComputerUse declares a Codex-only companion helper bundle that must be
	// repaired after Codex is locally re-signed.
	ComputerUse *ComputerUsePolicy
	// Updater selects the upstream update protocol for `<target> upgrade`.
	Updater Updater
}

// Registry is the canonical list of targets. Order matches the plan.
var Registry = []Target{
	{
		ID:       "cursor",
		AppPath:  "/Applications/Cursor.app",
		BundleID: "com.todesktop.230313mzl4w4u92",
		ExecName: "Cursor",
		Entitlements: &EntitlementsPolicy{
			RequiredBooleanEntitlements: []string{
				"com.apple.security.automation.apple-events",
				"com.apple.security.cs.disable-library-validation",
			},
		},
		KeychainServices: []string{"Cursor Safe Storage"},
		Updater: Updater{
			Kind:     UpdaterCursorManifest,
			Platform: "darwin-arm64",
			Product:  "cursor",
		},
	},
	{
		ID:       "codex",
		AppPath:  "/Applications/Codex.app",
		BundleID: "com.openai.codex",
		ExecName: "Codex",
		// AMFI refuses to load a binary that claims Team-bound entitlements
		// without a provisioning profile authorizing the claim. The original
		// Codex shipped with OpenAI's profile; after re-sign with Goodkind
		// the profile no longer authorizes anything, so the three Team-bound
		// keys must be stripped. Empirical: leaving them in produced
		// "AppleMobileFileIntegrityError Code=-413 No matching profile found"
		// from amfid and Launch Services refused to spawn the process.
		Entitlements: &EntitlementsPolicy{
			Strip: []string{
				"com.apple.application-identifier",
				"com.apple.developer.team-identifier",
				"keychain-access-groups",
			},
			RequiredBooleanEntitlements: []string{
				"com.apple.security.automation.apple-events",
				"com.apple.security.cs.disable-library-validation",
			},
		},
		KeychainServices: []string{"Codex Safe Storage", "Codex Auth", "Codex MCP Credentials"},
		NestedSignPaths: []string{
			"Contents/Resources/codex",
			"Contents/Resources/codex_chronicle",
			"Contents/Resources/node",
			"Contents/Resources/node_repl",
			"Contents/Resources/rg",
			"Contents/Resources/native/bare-modifier-monitor",
			"Contents/Resources/native/browser-use-peer-authorization.node",
			"Contents/Resources/native/devicecheck.node",
			"Contents/Resources/native/launch-services-helper",
			"Contents/Resources/native/remote-control-device-key.node",
			"Contents/Resources/native/sky.node",
			"Contents/Resources/native/sparkle.node",
			"Contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Downloader.xpc",
			"Contents/Frameworks/Sparkle.framework/Versions/B/XPCServices/Installer.xpc",
			"Contents/Frameworks/Sparkle.framework/Versions/B/Updater.app",
			"Contents/Frameworks/Sparkle.framework/Versions/B/Autoupdate",
			"Contents/Frameworks/Sparkle.framework",
		},
		ComputerUse: &ComputerUsePolicy{
			HostAppPath:           "/Applications/Codex.app",
			BundledAppPath:        "Contents/Resources/plugins/openai-bundled/plugins/computer-use/Codex Computer Use.app",
			AppPathFromHome:       ".codex/computer-use/Codex Computer Use.app",
			CacheAppGlobsFromHome: []string{".codex/plugins/cache/openai-bundled/computer-use/*/Codex Computer Use.app"},
			UpstreamTrustedTeamID: "2DC432GLL2",
			TeamPatchBinaries: []string{
				"Contents/MacOS/SkyComputerUseService",
				"Contents/SharedSupport/CUALockScreenGuardian.app/Contents/MacOS/CUALockScreenGuardian",
			},
			TeamRequirementPlists: []string{
				"Contents/SharedSupport/SkyComputerUseClient.app/Contents/Resources/SkyComputerUseClient_Parent.coderequirement",
				"Contents/SharedSupport/CUALockScreenGuardian.app/Contents/Resources/CUALockScreenGuardian_Parent.coderequirement",
			},
			SignTargets: []ComputerUseSignTarget{
				{
					Path: "Contents/SharedSupport/Codex Computer Use Installer.app",
				},
				{
					Path: "Contents/SharedSupport/SkyComputerUseClient.app",
					Entitlements: &EntitlementsPolicy{
						Strip: []string{
							"com.apple.security.application-groups",
						},
						RequiredBooleanEntitlements: []string{
							"com.apple.security.automation.apple-events",
						},
					},
				},
				{
					Path: "Contents/SharedSupport/CUALockScreenGuardian.app",
					Entitlements: &EntitlementsPolicy{
						Strip: []string{
							"com.apple.security.application-groups",
						},
					},
				},
				{
					Path: ".",
					Entitlements: &EntitlementsPolicy{
						Strip: []string{
							"com.apple.security.application-groups",
						},
						RequiredBooleanEntitlements: []string{
							"com.apple.security.automation.apple-events",
							"com.apple.security.device.audio-input",
						},
					},
				},
			},
		},
		Updater: Updater{
			Kind:             UpdaterSparkleAppcast,
			URL:              "https://persistent.oaistatic.com/codex-app-prod/appcast.xml",
			SparklePublicKey: "rhcBvttuqDFriyNqwTQJR3L4UT1WjIK4QxtwtwusVic=",
		},
	},
	{
		ID:       "claude",
		AppPath:  "/Applications/Claude.app",
		BundleID: "com.anthropic.claudefordesktop",
		ExecName: "Claude",
		Entitlements: &EntitlementsPolicy{
			Strip: []string{
				"com.apple.application-identifier",
				"com.apple.developer.team-identifier",
				"keychain-access-groups",
			},
			RequiredBooleanEntitlements: []string{
				"com.apple.security.cs.disable-library-validation",
			},
		},
		KeychainServices: []string{"Claude Safe Storage"},
		PreservedNestedCodePaths: []string{
			"Contents/Frameworks/Squirrel.framework",
		},
		Updater: Updater{
			Kind:              UpdaterClaudeSquirrel,
			URL:               "https://api.anthropic.com/api/desktop/darwin/universal/squirrel/update",
			DeviceIDParamName: "device_id",
		},
	},
}

// Lookup returns the target with the given ID, or an error if no such target
// is registered.
func Lookup(id string) (Target, error) {
	for _, t := range Registry {
		if t.ID == id {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("unknown target %q (known: cursor, codex, claude)", id)
}

// IDs returns the slugs of every registered target, in registry order.
func IDs() []string {
	ids := make([]string, 0, len(Registry))
	for _, t := range Registry {
		ids = append(ids, t.ID)
	}
	return ids
}
