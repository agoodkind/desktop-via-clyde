// Package devpreflight emits the shared, non-blocking development-signing
// credential check that both patch and upgrade run through the patch flow. When a
// target opts into development signing but its assets are missing, the check tells
// the operator either that App Store Connect credentials are present and the assets
// can be generated, or exactly which credential files to provide. It never blocks:
// a target with no credentials simply falls back to the standard shim plus
// Developer ID path, which leaves the codex device-key enrollment "-34018" fix
// unapplied.
package devpreflight

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/appleportal"
	"goodkind.io/desktop-via-clyde/internal/devsign"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

var devpreflightLog = slog.With("component", "desktop-via-clyde", "subcomponent", "devpreflight")

// CredentialsDiscoverer reports whether App Store Connect credentials are present.
// It defaults to appleportal.DiscoverCredentials and is overridable in tests so the
// check never touches ~/Desktop or contacts Apple.
var CredentialsDiscoverer = func() error {
	if _, err := appleportal.DiscoverCredentials(); err != nil {
		return fmt.Errorf("discover App Store Connect credentials: %w", err)
	}
	return nil
}

// AssetGenerator mints the missing development-signing assets through App Store
// Connect and returns the path of the generated provisioning profile. It defaults
// to appleportal.GenerateDevelopmentAssets and is overridable in tests so the
// generation path never contacts Apple. Only the opt-in auto_generate flag reaches
// it, so a target never calls App Store Connect implicitly.
var AssetGenerator = func(ctx context.Context, policy targets.DevelopmentSigningPolicy) (string, error) {
	result, err := appleportal.GenerateDevelopmentAssets(ctx, appleportal.DevelopmentAssetOptions{
		BundleID:    "",
		ProfileName: "",
		DeviceName:  "",
		DeviceUDID:  "",
		DestDir:     "",
		BaseName:    "",
	})
	if err != nil {
		return "", fmt.Errorf("generate development signing assets: %w", err)
	}
	return result.ProfilePath, nil
}

// Warn runs the non-blocking development-signing credential preflight for one
// target and writes any guidance to out. It returns nothing because it must never
// stop a patch or upgrade: callers invoke it for its side effect on out only. When
// the target opts into auto_generate and credentials are present, it mints the
// missing assets; any failure there is reported and swallowed so the patch still
// proceeds on the standard shim plus Developer ID path.
func Warn(ctx context.Context, out io.Writer, dryRun bool, t targets.Target) {
	if out == nil {
		out = os.Stdout
	}
	if t.DevelopmentSigning == nil || !t.DevelopmentSigning.Enabled {
		return
	}
	policy := *t.DevelopmentSigning
	missing := devsign.MissingAssets(policy)
	if len(missing) == 0 {
		return
	}
	devpreflightLog.DebugContext(ctx, "devpreflight.assets_missing", "target", t.ID, "missing", len(missing))

	missingSummary := summarizeMissing(missing)
	if err := CredentialsDiscoverer(); err != nil {
		warnCredentialsMissing(ctx, out, dryRun, t.ID, missingSummary)
		return
	}
	if policy.AutoGenerate && !dryRun {
		generateAssets(ctx, out, dryRun, t.ID, policy)
		return
	}
	notef(out, dryRun, fmt.Sprintf(
		"target=%s development signing enabled but assets are missing (%s); App Store Connect credentials are present, so set development_signing.auto_generate=true to mint these assets before patching",
		t.ID, missingSummary))
}

// generateAssets mints the missing assets and reports the outcome without ever
// returning an error: a failure falls back to the shim plus Developer ID path.
func generateAssets(ctx context.Context, out io.Writer, dryRun bool, targetID string, policy targets.DevelopmentSigningPolicy) {
	profilePath, err := AssetGenerator(ctx, policy)
	if err != nil {
		devpreflightLog.ErrorContext(ctx, "devpreflight.asset_generation_failed", "target", targetID, "err", err)
		notef(out, dryRun, fmt.Sprintf(
			"target=%s WARNING development-signing asset generation failed (%v); device-key enrollment falls back to the Developer ID strip (the -34018 keychain fix will not apply). Continuing.",
			targetID, err))
		return
	}
	notef(out, dryRun, fmt.Sprintf(
		"target=%s generated development-signing assets through App Store Connect (profile at %s); point development_signing paths at %s to apply the enrollment fix on the next patch",
		targetID, profilePath, appleportal.DevSigningDir()))
}

func warnCredentialsMissing(ctx context.Context, out io.Writer, dryRun bool, targetID, missingSummary string) {
	keyPath := devCertsPath("AuthKey_<KEY_ID>.p8")
	issuerPath := devCertsPath("README.md")
	devpreflightLog.DebugContext(ctx, "devpreflight.credentials_missing", "target", targetID)
	notef(out, dryRun, fmt.Sprintf(
		"target=%s WARNING development signing enabled but assets are missing (%s) and App Store Connect credentials were not found; provide the API key .p8 at %s and the issuer id in %s to generate them, otherwise device-key enrollment falls back to the Developer ID strip (the -34018 keychain fix will not apply). Continuing.",
		targetID, missingSummary, keyPath, issuerPath))
}

// devCertsPath builds the documented location of an App Store Connect credential
// file under ~/Desktop/dev-certs. It resolves the home directory rather than
// embedding a literal "~" because Go does not expand "~".
func devCertsPath(name string) string {
	homeDir, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(homeDir) == "" {
		homeDir = "$HOME"
	}
	return filepath.Join(homeDir, "Desktop", "dev-certs", name)
}

func summarizeMissing(missing []devsign.MissingAsset) string {
	parts := make([]string, 0, len(missing))
	for _, asset := range missing {
		parts = append(parts, asset.Label+" at "+asset.Path)
	}
	return strings.Join(parts, "; ")
}

func notef(out io.Writer, dryRun bool, message string) {
	prefix := "[run]"
	if dryRun {
		prefix = "[dry-run]"
	}
	fmt.Fprintf(out, "%s %s\n", prefix, message)
}
