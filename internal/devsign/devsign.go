// Package devsign applies the opt-in development-profile signing overlay that
// fixes codex device-key enrollment (the "-34018" errSecMissingEntitlement
// failure). It keeps the real Electron binary as the LaunchServices
// CFBundleExecutable, embeds a wildcard MAC_APP_DEVELOPMENT provisioning profile,
// applies Apple Development entitlements, and re-seals the bundle with
// keychain-free rcodesign. The shared proxy-injection helper installs a separate
// signed injector dylib under XDG state and points LSEnvironment at it.
package devsign

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"goodkind.io/desktop-via-clyde/internal/clioutput"
	shimembed "goodkind.io/desktop-via-clyde/internal/embed"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/signing"
	"goodkind.io/desktop-via-clyde/internal/spec"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

const (
	injectorDylibName   = "c.dylib"
	injectorPolicyName  = "policy.bin"
	embeddedProfileName = "embedded.provisionprofile"
	rcodesignRelPath    = ".cargo/bin/rcodesign"
	plistBuddyPath      = "/usr/libexec/PlistBuddy"
	// DyldInsertLibrariesKey is the LSEnvironment key that asks dyld to load
	// the injector.
	DyldInsertLibrariesKey = "DYLD_INSERT_LIBRARIES"
	// InjectorPolicyEnvKey is the LSEnvironment key that points the injector
	// at its policy.
	InjectorPolicyEnvKey = "DVC_CLYDE_INJECT_POLICY"
	// RedirectPortEnvKey is the injector policy key that enables connect
	// redirection to the target's Clyde listener port.
	RedirectPortEnvKey      = "DVC_CLYDE_REDIRECT_PORT"
	dyldInsertLibrariesPath = ":LSEnvironment:" + DyldInsertLibrariesKey
	injectorPolicyPlistPath = ":LSEnvironment:" + InjectorPolicyEnvKey
)

type policyAction string

const (
	policyActionAppend     policyAction = "append"
	policyActionAppendArgv policyAction = "append-argv"
	policyActionSet        policyAction = "set"
	policyActionUnset      policyAction = "unset"
)

//go:embed dev-entitlements.plist.tmpl
var devEntitlementsTemplate string

//go:embed testdata/smoke_host.c
var smokeHostSource string

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
// on disk. The injector override is optional because the repo builds and embeds
// the default injector; when an override path is set, it must exist.
func MissingAssets(policy targets.DevelopmentSigningPolicy) []MissingAsset {
	checks := []MissingAsset{
		{Label: "provisioning profile (profile_path)", Path: policy.ProfilePath},
		{Label: "signing identity p12 (p12_path)", Path: policy.P12Path},
		{Label: "p12 password file (p12_password_file)", Path: policy.P12PasswordFile},
	}
	if policy.ProxyInjection && strings.TrimSpace(policy.InjectorDylibPath) != "" {
		checks = append(checks, MissingAsset{Label: "injector dylib override (injector_dylib_path)", Path: policy.InjectorDylibPath})
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

// Plan carries the inputs that the final --shallow top-bundle reseal needs. It
// is produced by ApplyNestedMutations and consumed by Reseal so the caller can
// defer the reseal until after every nested re-signing step has run.
type Plan struct {
	rcodesign string
	p12Args   []string
	entFile   string
}

// InjectorPath returns the external state path for a target's injector dylib.
func InjectorPath(t targets.Target) string {
	return filepath.Join(paths.StateRoot(), "dev-signing", "injectors", t.ID, injectorDylibName)
}

// InjectorPolicyPath returns the external state path for a target's injector
// policy file.
func InjectorPolicyPath(t targets.Target) string {
	return filepath.Join(paths.StateRoot(), "dev-signing", "injectors", t.ID, injectorPolicyName)
}

// AppLocalInjectorPath returns the obsolete in-bundle injector path.
func AppLocalInjectorPath(t targets.Target) string {
	return filepath.Join(paths.MacOSDir(t), injectorDylibName)
}

// ApplyProxyInjection installs and signs the injector without changing the
// caller's bundle signing strategy.
func ApplyProxyInjection(ctx context.Context, cmd Commander, opts Options, t targets.Target) error {
	if t.DevelopmentSigning == nil || !t.DevelopmentSigning.ProxyInjection {
		return nil
	}
	return installInjector(ctx, cmd, opts, t, paths.InfoPlistPath(t))
}

// ApplyNestedMutations runs every development-signing mutation except the final
// --shallow top-bundle reseal.
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

	infoPlist := paths.InfoPlistPath(t)
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
		if err := installInjector(ctx, cmd, opts, t, infoPlist); err != nil {
			return nil, err
		}
	}
	entFile, err := writeDevelopmentEntitlements(ctx, opts, t, teamID)
	if err != nil {
		return nil, err
	}
	p12Args := []string{"--p12-file", policy.P12Path, "--p12-password-file", policy.P12PasswordFile}
	return &Plan{rcodesign: rcodesign, p12Args: p12Args, entFile: entFile}, nil
}

// Reseal performs the final --shallow top-bundle reseal and strips quarantine.
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

func installInjector(ctx context.Context, cmd Commander, opts Options, t targets.Target, infoPlist string) error {
	dylibDest := InjectorPath(t)
	policyPath := InjectorPolicyPath(t)
	tempDylib := dylibDest + ".tmp"
	devsignLog.DebugContext(ctx, "devsign.install_injector.boundary",
		"target", t.ID,
		"dylib", dylibDest,
		"policy", policyPath,
		"dry_run", opts.DryRun)

	if err := writeInjectorPolicy(ctx, opts, t, policyPath); err != nil {
		return err
	}
	if err := stageInjectorDylib(ctx, cmd, opts, t, tempDylib); err != nil {
		return err
	}
	if err := signInjector(ctx, cmd, opts, t, tempDylib); err != nil {
		return err
	}
	if err := verifyInjector(ctx, cmd, opts, t, tempDylib, policyPath); err != nil {
		return err
	}
	note(opts, fmt.Sprintf("target=%s development signing: install external injector %s -> %s", t.ID, tempDylib, dylibDest))
	if !opts.DryRun {
		if err := os.Rename(tempDylib, dylibDest); err != nil {
			return logDevsignError(ctx, "devsign.install_external_injector_failed", fmt.Errorf("rename injector into place: %w", err))
		}
	}
	if err := removeAppLocalInjector(ctx, cmd, opts, t); err != nil {
		return err
	}
	if err := setInjectorEnvironment(ctx, cmd, opts, t, infoPlist, dylibDest, policyPath); err != nil {
		return err
	}
	return nil
}

func writeInjectorPolicy(ctx context.Context, opts Options, t targets.Target, policyPath string) error {
	devsignLog.DebugContext(ctx, "devsign.write_injector_policy.boundary",
		"target", t.ID,
		"policy", policyPath,
		"dry_run", opts.DryRun)
	note(opts, fmt.Sprintf("target=%s development signing: write injector policy -> %s", t.ID, policyPath))
	if opts.DryRun {
		return nil
	}
	data, err := EncodeInjectorPolicy(t.LaunchPolicy)
	if err != nil {
		return logDevsignError(ctx, "devsign.injector_policy_encode_failed", err)
	}
	if err := os.MkdirAll(filepath.Dir(policyPath), 0o700); err != nil {
		return logDevsignError(ctx, "devsign.injector_policy_dir_failed", fmt.Errorf("create injector policy dir: %w", err))
	}
	if err := os.WriteFile(policyPath, data, 0o600); err != nil {
		return logDevsignError(ctx, "devsign.injector_policy_write_failed", fmt.Errorf("write injector policy: %w", err))
	}
	return nil
}

func stageInjectorDylib(ctx context.Context, cmd Commander, opts Options, t targets.Target, tempDylib string) error {
	overridePath := strings.TrimSpace(t.DevelopmentSigning.InjectorDylibPath)
	devsignLog.DebugContext(ctx, "devsign.stage_injector.boundary",
		"target", t.ID,
		"temp_dylib", tempDylib,
		"override", overridePath != "",
		"dry_run", opts.DryRun)
	note(opts, fmt.Sprintf("target=%s development signing: stage external injector -> %s", t.ID, tempDylib))
	if opts.DryRun {
		if overridePath != "" {
			if err := cmd.Run(ctx, "/bin/cp", "-f", overridePath, tempDylib); err != nil {
				return logDevsignError(ctx, "devsign.stage_injector_override_dry_run_failed", fmt.Errorf("stage injector override: %w", err))
			}
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(tempDylib), 0o700); err != nil {
		return logDevsignError(ctx, "devsign.injector_dir_failed", fmt.Errorf("create injector dir: %w", err))
	}
	if overridePath != "" {
		if err := cmd.Run(ctx, "/bin/cp", "-f", overridePath, tempDylib); err != nil {
			return logDevsignError(ctx, "devsign.stage_injector_override_failed", fmt.Errorf("stage injector override: %w", err))
		}
		return nil
	}
	if len(shimembed.InjectorDylib) == 0 {
		return logDevsignError(ctx, "devsign.embedded_injector_missing", fmt.Errorf("embedded injector is empty; run `make injector-build` before building"))
	}
	if err := os.WriteFile(tempDylib, shimembed.InjectorDylib, 0o600); err != nil {
		return logDevsignError(ctx, "devsign.stage_embedded_injector_failed", fmt.Errorf("write embedded injector: %w", err))
	}
	if err := os.Chmod(tempDylib, 0o755); err != nil {
		return logDevsignError(ctx, "devsign.chmod_embedded_injector_failed", fmt.Errorf("chmod embedded injector: %w", err))
	}
	return nil
}

func signInjector(ctx context.Context, cmd Commander, opts Options, t targets.Target, dylibPath string) error {
	id, err := signing.ResolveIdentity(ctx, opts.DryRun)
	if err != nil {
		return logDevsignError(ctx, "devsign.resolve_injector_signing_identity_failed", fmt.Errorf("resolve injector signing identity: %w", err))
	}
	note(opts, fmt.Sprintf("target=%s development signing: codesign external injector %s", t.ID, dylibPath))
	if err := cmd.Run(ctx, "/usr/bin/codesign", signing.RuntimeArgs(id, dylibPath)...); err != nil {
		return logDevsignError(ctx, "devsign.sign_injector_failed", fmt.Errorf("sign external injector: %w", err))
	}
	return nil
}

func verifyInjector(ctx context.Context, cmd Commander, opts Options, t targets.Target, dylibPath string, policyPath string) error {
	note(opts, fmt.Sprintf("target=%s development signing: verify external injector %s", t.ID, dylibPath))
	if err := cmd.Run(ctx, "/usr/bin/codesign", "--verify", "--strict", "--verbose=2", dylibPath); err != nil {
		return logDevsignError(ctx, "devsign.verify_injector_signature_failed", fmt.Errorf("verify external injector signature: %w", err))
	}
	if opts.DryRun {
		return nil
	}
	if err := SmokeTestInjector(ctx, dylibPath, policyPath); err != nil {
		return logDevsignError(ctx, "devsign.smoke_injector_failed", fmt.Errorf("smoke-test external injector: %w", err))
	}
	return nil
}

func removeAppLocalInjector(ctx context.Context, cmd Commander, opts Options, t targets.Target) error {
	localPath := AppLocalInjectorPath(t)
	note(opts, fmt.Sprintf("target=%s development signing: remove stale app-local injector %s", t.ID, localPath))
	if err := cmd.Run(ctx, "/bin/rm", "-f", localPath); err != nil {
		return logDevsignError(ctx, "devsign.remove_app_local_injector_failed", fmt.Errorf("remove stale app-local injector: %w", err))
	}
	return nil
}

func setInjectorEnvironment(ctx context.Context, cmd Commander, opts Options, t targets.Target, infoPlist string, dylibPath string, policyPath string) error {
	note(opts, fmt.Sprintf("target=%s development signing: set Info.plist injector env", t.ID))
	_ = cmd.Run(ctx, plistBuddyPath, "-c", "Delete :LSEnvironment", infoPlist)
	if err := cmd.Run(ctx, plistBuddyPath, "-c", "Add :LSEnvironment dict", infoPlist); err != nil {
		return logDevsignError(ctx, "devsign.add_lsenvironment_failed", fmt.Errorf("add LSEnvironment dict: %w", err))
	}
	if err := cmd.Run(ctx, plistBuddyPath, "-c", "Add "+dyldInsertLibrariesPath+" string "+dylibPath, infoPlist); err != nil {
		return logDevsignError(ctx, "devsign.set_dyld_insert_failed", fmt.Errorf("set %s: %w", DyldInsertLibrariesKey, err))
	}
	if err := cmd.Run(ctx, plistBuddyPath, "-c", "Add "+injectorPolicyPlistPath+" string "+policyPath, infoPlist); err != nil {
		return logDevsignError(ctx, "devsign.set_injector_policy_failed", fmt.Errorf("set %s: %w", InjectorPolicyEnvKey, err))
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

func resealBundle(ctx context.Context, cmd Commander, opts Options, t targets.Target, rcodesign string, p12Args []string, entFile string) error {
	note(opts, fmt.Sprintf("target=%s development signing: rcodesign --shallow reseal (Apple Development cert), nested code keeps Developer ID", t.ID))
	args := append([]string{"sign"}, p12Args...)
	args = append(args, "--shallow")
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
	_ = cmd.Run(ctx, "/usr/bin/xattr", "-dr", "com.apple.quarantine", t.AppPath)
	return nil
}

// EncodeInjectorPolicy writes the compact NUL-separated policy consumed by the
// C injector.
func EncodeInjectorPolicy(policy spec.LaunchPolicySpec) ([]byte, error) {
	var buffer bytes.Buffer
	hasRedirectPortAction := false
	for _, envAction := range policy.Environment {
		key := strings.TrimSpace(envAction.Key)
		if key == RedirectPortEnvKey {
			hasRedirectPortAction = true
		}
		switch policyAction(strings.ToLower(strings.TrimSpace(envAction.Action))) {
		case policyActionSet:
			if key == "" {
				return nil, fmt.Errorf("injector policy set action has empty key")
			}
			writePolicyRecord(&buffer, string(policyActionSet), key, envAction.Value)
		case policyActionUnset:
			if key == "" {
				return nil, fmt.Errorf("injector policy unset action has empty key")
			}
			writePolicyRecord(&buffer, string(policyActionUnset), key)
		case policyActionAppend, policyActionAppendArgv:
			return nil, fmt.Errorf("unsupported injector env action %q", envAction.Action)
		default:
			return nil, fmt.Errorf("unsupported injector env action %q", envAction.Action)
		}
	}
	if policy.ProxyPort > 0 && !hasRedirectPortAction {
		writePolicyRecord(&buffer, string(policyActionSet), RedirectPortEnvKey, strconv.Itoa(policy.ProxyPort))
	}
	for _, argAction := range policy.Arguments {
		switch policyAction(strings.ToLower(strings.TrimSpace(argAction.Action))) {
		case policyActionAppend:
			if argAction.Value == "" {
				return nil, fmt.Errorf("injector policy append action has empty value")
			}
			writePolicyRecord(&buffer, string(policyActionAppendArgv), argAction.Value)
		case policyActionAppendArgv, policyActionSet, policyActionUnset:
			return nil, fmt.Errorf("unsupported injector arg action %q", argAction.Action)
		default:
			return nil, fmt.Errorf("unsupported injector arg action %q", argAction.Action)
		}
	}
	return buffer.Bytes(), nil
}

func writePolicyRecord(buffer *bytes.Buffer, values ...string) {
	for _, value := range values {
		buffer.WriteString(value)
		buffer.WriteByte(0)
	}
}

// SmokeTestInjector proves dyld can load the injector, keeps DYLD propagation
// enabled for child processes, and applies policy actions.
func SmokeTestInjector(ctx context.Context, dylibPath string, policyPath string) error {
	devsignLog.DebugContext(ctx, "devsign.smoke_injector.boundary",
		"dylib", dylibPath,
		"policy", policyPath)
	tempDir, err := os.MkdirTemp("", "desktop-via-clyde-injector-smoke-*")
	if err != nil {
		return logDevsignError(ctx, "devsign.smoke_create_dir_failed", fmt.Errorf("create injector smoke dir: %w", err))
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	sourcePath := filepath.Join(tempDir, "host.c")
	hostDir := filepath.Join(tempDir, "FakeApp.app", "Contents", "MacOS")
	hostPath := filepath.Join(hostDir, "host")
	if err := os.MkdirAll(hostDir, 0o700); err != nil {
		return logDevsignError(ctx, "devsign.smoke_create_host_dir_failed", fmt.Errorf("create injector smoke host dir: %w", err))
	}
	if err := os.WriteFile(sourcePath, []byte(smokeHostSource), 0o600); err != nil {
		return logDevsignError(ctx, "devsign.smoke_write_host_failed", fmt.Errorf("write injector smoke host source: %w", err))
	}
	compile := exec.CommandContext(ctx, "/usr/bin/xcrun", "clang", "-Wall", "-Wextra", "-Werror", "-o", hostPath, sourcePath)
	if output, err := compile.CombinedOutput(); err != nil {
		return logDevsignError(ctx, "devsign.smoke_compile_host_failed", fmt.Errorf("compile injector smoke host: %w (output: %s)", err, string(output)))
	}
	if err := runInjectorSmokeHost(ctx, hostPath, dylibPath, policyPath, nil); err != nil {
		return err
	}
	sentinelPolicyPath := filepath.Join(tempDir, "sentinel-policy.bin")
	var sentinelPolicy bytes.Buffer
	writePolicyRecord(&sentinelPolicy, string(policyActionSet), "DVC_INJECT_SMOKE_SET", "ok")
	writePolicyRecord(&sentinelPolicy, string(policyActionUnset), "DVC_INJECT_SMOKE_REMOVE")
	writePolicyRecord(&sentinelPolicy, string(policyActionAppendArgv), "--dvc-inject-smoke-arg")
	if err := os.WriteFile(sentinelPolicyPath, sentinelPolicy.Bytes(), 0o600); err != nil {
		return logDevsignError(ctx, "devsign.smoke_write_policy_failed", fmt.Errorf("write injector smoke policy: %w", err))
	}
	return runInjectorSmokeHost(ctx, hostPath, dylibPath, sentinelPolicyPath, []string{
		"DVC_INJECT_SMOKE_MODE=sentinel",
		"DVC_INJECT_SMOKE_REMOVE=present",
	})
}

func runInjectorSmokeHost(ctx context.Context, hostPath string, dylibPath string, policyPath string, extraEnv []string) error {
	devsignLog.DebugContext(ctx, "devsign.run_injector_smoke_host.boundary",
		"host", hostPath,
		"dylib", dylibPath,
		"policy", policyPath)
	run := exec.CommandContext(ctx, hostPath)
	run.Env = append(os.Environ(), extraEnv...)
	run.Env = append(run.Env,
		DyldInsertLibrariesKey+"="+dylibPath,
		InjectorPolicyEnvKey+"="+policyPath,
	)
	if output, err := run.CombinedOutput(); err != nil {
		return logDevsignError(ctx, "devsign.smoke_run_host_failed", fmt.Errorf("run injector smoke host: %w (output: %s)", err, string(output)))
	}
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
