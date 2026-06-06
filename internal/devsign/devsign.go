// Package devsign applies the opt-in development-profile signing overlay that
// fixes codex device-key enrollment (the "-34018" errSecMissingEntitlement
// failure). It mirrors the proven spike recipe in
// /tmp/dvc-spike/enroll-proxy-overlay.sh: keep the real Electron binary as the
// LaunchServices CFBundleExecutable (no shim, no move-to-.real), embed a wildcard
// MAC_APP_DEVELOPMENT provisioning profile, apply Apple Development entitlements
// that carry the team-restricted keychain-access-groups, and re-seal the bundle
// with rcodesign (keychain-free) so the device-key SecItemAdd succeeds at runtime.
// Optionally it loads an injector dylib via DYLD_INSERT_LIBRARIES to restore
// clyde proxy routing without touching the Codex Framework binary (re-signing the
// Framework crashes V8).
package devsign

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

const (
	injectorDylibName       = "c.dylib"
	embeddedProfileName     = "embedded.provisionprofile"
	rcodesignRelPath        = ".cargo/bin/rcodesign"
	plistBuddyPath          = "/usr/libexec/PlistBuddy"
	dyldInsertLibrariesKey  = "DYLD_INSERT_LIBRARIES"
	dyldInsertLibrariesPath = ":LSEnvironment:" + dyldInsertLibrariesKey
)

//go:embed dev-entitlements.plist.tmpl
var devEntitlementsTemplate string

var devsignLog = slog.With("component", "desktop-via-clyde", "subcomponent", "devsign")

func logDevsignError(ctx context.Context, event string, err error) error {
	devsignLog.ErrorContext(ctx, event, "err", err)
	return err
}

func logDevsignErrorNoContext(event string, err error) error {
	devsignLog.Error(event, "err", err)
	return err
}

// Commander runs external commands. *patch.Runner satisfies it, which keeps the
// dry-run handling and command tracing in one place and avoids an import cycle
// between this package and internal/patch.
type Commander interface {
	Run(ctx context.Context, name string, args ...string) error
}

// Options controls how the overlay is applied.
type Options struct {
	DryRun   bool
	Out      io.Writer
	Progress clioutput.Progress
}

// MissingAsset names one required development-signing input that is absent.
type MissingAsset struct {
	Label string
	Path  string
}

// MissingAssets returns the development-signing inputs that are unset or absent
// on disk. An empty result means the overlay can run; otherwise the caller emits
// a non-blocking warning naming each file and falls back to the shim path. The
// injector dylib is only required when proxy injection is requested.
func MissingAssets(policy targets.DevelopmentSigningPolicy) []MissingAsset {
	checks := []MissingAsset{
		{Label: "provisioning profile (profile_path)", Path: policy.ProfilePath},
		{Label: "signing identity p12 (p12_path)", Path: policy.P12Path},
		{Label: "p12 password file (p12_password_file)", Path: policy.P12PasswordFile},
	}
	if policy.ProxyInjection {
		checks = append(checks, MissingAsset{Label: "injector dylib (injector_dylib_path)", Path: policy.InjectorDylibPath})
	}
	missing := make([]MissingAsset, 0, len(checks))
	for _, check := range checks {
		if strings.TrimSpace(check.Path) == "" {
			missing = append(missing, MissingAsset{Label: check.Label, Path: "(unset)"})
			continue
		}
		if _, err := os.Stat(check.Path); err != nil {
			missing = append(missing, check)
		}
	}
	return missing
}

type entitlementsData struct {
	TeamID         string
	BundleID       string
	ProxyInjection bool
}

// Plan carries the inputs that the final --shallow top-bundle reseal needs. It is
// produced by ApplyNestedMutations and consumed by Reseal so the caller can defer
// the reseal until after every nested re-signing step has run.
type Plan struct {
	rcodesign string
	p12Args   []string
	entFile   string
}

// ApplyNestedMutations runs every development-signing mutation except the final
// --shallow top-bundle reseal: it drops the *.app.in cruft, embeds the profile,
// places and signs the injector dylib, edits Info.plist, and writes the
// entitlements file. The caller must have validated assets with MissingAssets
// first. None of these steps moves the main binary to .real or installs the shim:
// the real Electron binary remains the LaunchServices executable, which is
// required for amfi to associate the embedded MAC_APP_DEVELOPMENT profile with the
// running process. The returned Plan must be passed to Reseal as the patch's last
// signing action.
func ApplyNestedMutations(ctx context.Context, cmd Commander, opts Options, t targets.Target) (*Plan, error) {
	policy := t.DevelopmentSigning
	devsignLog.DebugContext(ctx, "devsign.apply_nested.boundary", "target", t.ID, "dry_run", opts.DryRun)
	if policy == nil {
		return nil, logDevsignError(ctx, "devsign.policy_missing", fmt.Errorf("development signing policy is nil for target %s", t.ID))
	}
	teamID := strings.TrimSpace(paths.SignTeamID())
	if teamID == "" {
		return nil, logDevsignError(ctx, "devsign.team_id_missing", fmt.Errorf("development signing for target %s requires a configured signing team_id", t.ID))
	}

	macOSDir := paths.MacOSDir(t)
	infoPlist := paths.InfoPlistPath(t)
	dylibDest := filepath.Join(macOSDir, injectorDylibName)
	profileDest := filepath.Join(t.AppPath, "Contents", embeddedProfileName)
	rcodesign := filepath.Join(paths.Home(), filepath.FromSlash(rcodesignRelPath))

	note(opts, fmt.Sprintf("target=%s development signing: keep real executable as CFBundleExecutable (no shim, no move-to-real)", t.ID))

	if err := removeAppInCruft(ctx, cmd, opts, t); err != nil {
		return nil, err
	}
	if err := embedProvisioningProfile(ctx, cmd, opts, t, profileDest); err != nil {
		return nil, err
	}
	if policy.ProxyInjection {
		if err := installInjector(ctx, cmd, opts, t, dylibDest, infoPlist); err != nil {
			return nil, err
		}
	}
	entFile, err := writeDevelopmentEntitlements(ctx, opts, t, teamID)
	if err != nil {
		return nil, err
	}
	p12Args := []string{"--p12-file", policy.P12Path, "--p12-password-file", policy.P12PasswordFile}
	if policy.ProxyInjection {
		if err := signInjector(ctx, cmd, opts, t, rcodesign, p12Args, dylibDest); err != nil {
			return nil, err
		}
	}
	return &Plan{rcodesign: rcodesign, p12Args: p12Args, entFile: entFile}, nil
}

// Reseal performs the final --shallow top-bundle reseal and strips quarantine. It
// must run as the patch's last signing action: the --shallow reseal records the
// cdhashes of every nested object into the top bundle's CodeResources, so any
// later re-sign of nested code would invalidate the seal.
func Reseal(ctx context.Context, cmd Commander, opts Options, t targets.Target, plan *Plan) error {
	devsignLog.DebugContext(ctx, "devsign.reseal.boundary", "target", t.ID, "dry_run", opts.DryRun)
	if plan == nil {
		return logDevsignError(ctx, "devsign.reseal_plan_missing", fmt.Errorf("development signing reseal plan is nil for target %s", t.ID))
	}
	if err := resealBundle(ctx, cmd, opts, t, plan.rcodesign, plan.p12Args, plan.entFile); err != nil {
		return err
	}
	return stripQuarantine(ctx, cmd, opts, t)
}

func removeAppInCruft(ctx context.Context, cmd Commander, opts Options, t targets.Target) error {
	note(opts, fmt.Sprintf("target=%s development signing: remove unsignable *.app.in cruft", t.ID))
	if err := cmd.Run(ctx, "/usr/bin/find", t.AppPath, "-name", "*.app.in", "-exec", "/bin/rm", "-rf", "{}", "+"); err != nil {
		return logDevsignError(ctx, "devsign.remove_app_in_failed", fmt.Errorf("remove *.app.in cruft: %w", err))
	}
	return nil
}

func embedProvisioningProfile(ctx context.Context, cmd Commander, opts Options, t targets.Target, profileDest string) error {
	note(opts, fmt.Sprintf("target=%s development signing: embed provisioning profile %s -> %s", t.ID, t.DevelopmentSigning.ProfilePath, profileDest))
	if err := cmd.Run(ctx, "/bin/cp", "-f", t.DevelopmentSigning.ProfilePath, profileDest); err != nil {
		return logDevsignError(ctx, "devsign.embed_profile_failed", fmt.Errorf("embed provisioning profile: %w", err))
	}
	return nil
}

func installInjector(ctx context.Context, cmd Commander, opts Options, t targets.Target, dylibDest string, infoPlist string) error {
	note(opts, fmt.Sprintf("target=%s development signing: place injector dylib %s -> %s", t.ID, t.DevelopmentSigning.InjectorDylibPath, dylibDest))
	if err := cmd.Run(ctx, "/bin/cp", "-f", t.DevelopmentSigning.InjectorDylibPath, dylibDest); err != nil {
		return logDevsignError(ctx, "devsign.place_injector_failed", fmt.Errorf("place injector dylib: %w", err))
	}
	note(opts, fmt.Sprintf("target=%s development signing: set Info.plist %s=%s", t.ID, dyldInsertLibrariesPath, dylibDest))
	// Delete any existing LSEnvironment dict best-effort, then recreate it with
	// the injector path. PlistBuddy cannot add a key to a dict that is absent and
	// cannot add a dict that already exists, so the delete-then-add sequence
	// matches the validated spike recipe.
	_ = cmd.Run(ctx, plistBuddyPath, "-c", "Delete :LSEnvironment", infoPlist)
	if err := cmd.Run(ctx, plistBuddyPath, "-c", "Add :LSEnvironment dict", infoPlist); err != nil {
		return logDevsignError(ctx, "devsign.add_lsenvironment_failed", fmt.Errorf("add LSEnvironment dict: %w", err))
	}
	if err := cmd.Run(ctx, plistBuddyPath, "-c", "Add "+dyldInsertLibrariesPath+" string "+dylibDest, infoPlist); err != nil {
		return logDevsignError(ctx, "devsign.set_dyld_insert_failed", fmt.Errorf("set %s: %w", dyldInsertLibrariesKey, err))
	}
	return nil
}

func writeDevelopmentEntitlements(ctx context.Context, opts Options, t targets.Target, teamID string) (string, error) {
	entFile := filepath.Join(os.TempDir(), "dvc-devsign-"+t.ID+"-ent.plist")
	devsignLog.DebugContext(ctx, "devsign.write_entitlements.boundary", "target", t.ID, "path", entFile, "dry_run", opts.DryRun)
	note(opts, fmt.Sprintf("target=%s development signing: write entitlements (app-id=%s.%s, keychain-access-groups=%s.*) -> %s",
		t.ID, teamID, t.BundleID, teamID, entFile))
	if opts.DryRun {
		return entFile, nil
	}
	rendered, err := renderEntitlements(entitlementsData{
		TeamID:         teamID,
		BundleID:       t.BundleID,
		ProxyInjection: t.DevelopmentSigning.ProxyInjection,
	})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(entFile, rendered, 0o600); err != nil {
		return "", logDevsignErrorNoContext("devsign.write_entitlements_failed", fmt.Errorf("write development entitlements %s: %w", entFile, err))
	}
	return entFile, nil
}

func renderEntitlements(data entitlementsData) ([]byte, error) {
	parsed, err := template.New("dev-entitlements").Parse(devEntitlementsTemplate)
	if err != nil {
		return nil, logDevsignErrorNoContext("devsign.parse_entitlements_template_failed", fmt.Errorf("parse development entitlements template: %w", err))
	}
	var buffer bytes.Buffer
	if err := parsed.Execute(&buffer, data); err != nil {
		return nil, logDevsignErrorNoContext("devsign.render_entitlements_failed", fmt.Errorf("render development entitlements: %w", err))
	}
	return buffer.Bytes(), nil
}

func signInjector(ctx context.Context, cmd Commander, opts Options, t targets.Target, rcodesign string, p12Args []string, dylibDest string) error {
	note(opts, fmt.Sprintf("target=%s development signing: rcodesign sign injector dylib %s", t.ID, dylibDest))
	args := append([]string{"sign"}, p12Args...)
	args = append(args, "--code-signature-flags", "runtime", dylibDest)
	if err := cmd.Run(ctx, rcodesign, args...); err != nil {
		return logDevsignError(ctx, "devsign.sign_injector_failed", fmt.Errorf("sign injector dylib: %w", err))
	}
	return nil
}

func resealBundle(ctx context.Context, cmd Commander, opts Options, t targets.Target, rcodesign string, p12Args []string, entFile string) error {
	note(opts, fmt.Sprintf("target=%s development signing: rcodesign --shallow reseal (Apple Development cert), nested code keeps Developer ID", t.ID))
	args := append([]string{"sign"}, p12Args...)
	args = append(args, "--shallow")
	// Exclude the Computer Use app: rcodesign 0.29 otherwise descends into it and
	// fails on its unsigned resource .bundles. The path comes from config, not a
	// hardcoded literal.
	if cu := t.Extensions.ComputerUse; cu != nil && strings.TrimSpace(cu.BundledAppPath) != "" {
		args = append(args, "--exclude", cu.BundledAppPath)
	}
	args = append(args, "--entitlements-xml-file", entFile, "--code-signature-flags", "runtime", t.AppPath)
	if err := cmd.Run(ctx, rcodesign, args...); err != nil {
		return logDevsignError(ctx, "devsign.reseal_bundle_failed", fmt.Errorf("reseal bundle: %w", err))
	}
	return nil
}

func stripQuarantine(ctx context.Context, cmd Commander, opts Options, t targets.Target) error {
	note(opts, fmt.Sprintf("target=%s development signing: remove quarantine attribute (best effort)", t.ID))
	// Best effort: a bundle that was never quarantined has no attribute to remove.
	_ = cmd.Run(ctx, "/usr/bin/xattr", "-dr", "com.apple.quarantine", t.AppPath)
	return nil
}

func note(opts Options, message string) {
	if opts.Progress != nil {
		opts.Progress.Step(message)
		return
	}
	if opts.Out == nil {
		return
	}
	prefix := "[run]"
	if opts.DryRun {
		prefix = "[dry-run]"
	}
	fmt.Fprintf(opts.Out, "%s %s\n", prefix, message)
}
