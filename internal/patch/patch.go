// Package patch implements the patch, unpatch, keychain-migrate, and drift
// re-apply workflows.
package patch

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	shimembed "goodkind.io/desktop-via-clyde/internal/embed"
	"goodkind.io/desktop-via-clyde/internal/launchagent"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// Options controls a patch invocation.
type Options struct {
	DryRun            bool
	NoMigrateKeychain bool
	SkipLaunchAgent   bool
	Out               io.Writer
}

// BundleOptions controls a PatchExtractedBundle invocation. Unlike
// Options on the full Patch flow, BundleOptions never touches the
// keychain, never writes state.json, never installs the LaunchAgent,
// and never runs the post-patch verify. The clyde MITM hook
// subprocess uses it to re-patch a freshly downloaded update bundle
// inside a staging directory before clyde streams the bytes back to
// Squirrel.Mac for installation.
type BundleOptions struct {
	DryRun bool
	Out    io.Writer
}

// Patch performs the full patch flow for one target. Steps are numbered to
// match the plan: 1, 1b, 2..7, 7a, 8..10.
func Patch(t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	r := NewRunner(opts.DryRun, opts.Out)

	if !opts.DryRun {
		if _, err := os.Stat(t.AppPath); err != nil {
			return fmt.Errorf("bundle not found at %s: %w", t.AppPath, err)
		}
	}

	info, err := loadInfoPlistOrPlaceholder(t, opts.DryRun)
	if err != nil {
		return err
	}
	r.Note("target=%s step 0: read Info.plist version=%s id=%s exec=%s",
		t.ID, info.CFBundleVersion, info.CFBundleIdentifier, info.CFBundleExecutable)

	// Step 1: backup.
	if err := stepBackup(r, t); err != nil {
		return fmt.Errorf("backup: %w", err)
	}

	// Step 1b: keychain capture (in-memory).
	var captured []KeychainItem
	if opts.NoMigrateKeychain {
		r.Note("target=%s step 1b: skipped (--no-migrate-keychain)", t.ID)
	} else if opts.DryRun {
		r.Note("target=%s step 1b: would capture keychain items for services=%v", t.ID, t.KeychainServices)
	} else {
		captured, err = CaptureItems(t)
		if err != nil {
			return fmt.Errorf("keychain capture: %w", err)
		}
		r.Note("target=%s step 1b: captured %d keychain items", t.ID, len(captured))
	}

	// Steps 2-7: bundle mutation (entitlements, exec rename, shim
	// install, re-sign, strip quarantine). Shared with the
	// mitm-hook re-patch flow so initial-install and update-time
	// patches always run the same bundle-mutation logic.
	if err := patchBundleSteps(r, t); err != nil {
		return err
	}

	if err := stepPatchComputerUse(r, t); err != nil {
		return fmt.Errorf("patch computer use helper: %w", err)
	}

	// Step 7a: keychain re-grant.
	if opts.NoMigrateKeychain {
		r.Note("target=%s step 7a: skipped (--no-migrate-keychain)", t.ID)
	} else if opts.DryRun {
		r.Note("target=%s step 7a: would re-grant ACLs on captured keychain items", t.ID)
	} else if len(captured) > 0 {
		if err := RegrantItems(t, captured); err != nil {
			r.Note("target=%s step 7a: re-grant returned errors (continuing): %v", t.ID, err)
		} else {
			r.Note("target=%s step 7a: re-granted ACLs on %d keychain items", t.ID, len(captured))
		}
	} else {
		r.Note("target=%s step 7a: no captured items, nothing to re-grant", t.ID)
	}

	// Step 8: update state.json.
	if err := stepWriteState(r, t, info.CFBundleVersion); err != nil {
		return fmt.Errorf("write state: %w", err)
	}

	// Step 9: install + load LaunchAgent if absent.
	if opts.SkipLaunchAgent {
		r.Note("step 9: skipped LaunchAgent install/load")
	} else if err := stepInstallLaunchAgent(r); err != nil {
		return fmt.Errorf("install LaunchAgent: %w", err)
	}

	// Step 10: verify.
	if err := stepVerify(r, t); err != nil {
		return fmt.Errorf("verify: %w", err)
	}

	r.Note("target=%s patch complete", t.ID)
	return nil
}

// PatchExtractedBundle runs the bundle-mutation steps against the
// .app at t.AppPath: extract entitlements from the existing main
// binary, augment them, rename the main executable to .real, install
// the embedded shim in the original slot, re-sign all three layers
// (.real, shim, outer bundle) with the user's Developer ID, and strip
// the quarantine xattr. The function skips backup, keychain
// migration, state.json updates, the LaunchAgent install, and the
// post-patch verify, since the clyde MITM hook subprocess that calls
// this entry point patches a freshly downloaded update bundle inside
// a staging directory rather than /Applications.
//
// Idempotent: re-running against an already-patched bundle preserves
// <ExecName>.real and just refreshes the embedded shim plus
// signatures.
func PatchExtractedBundle(t targets.Target, opts BundleOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	r := NewRunner(opts.DryRun, opts.Out)

	if !opts.DryRun {
		if _, err := os.Stat(t.AppPath); err != nil {
			return fmt.Errorf("bundle not found at %s: %w", t.AppPath, err)
		}
	}
	return patchBundleSteps(r, t)
}

// patchBundleSteps runs the bundle-mutation steps (2 through 7) on
// the bundle at t.AppPath using the supplied Runner. Shared between
// the install-time Patch flow and the update-time
// PatchExtractedBundle flow so both paths always run the same
// signing logic.
func patchBundleSteps(r *Runner, t targets.Target) error {
	if t.Entitlements == nil {
		return fmt.Errorf("target %s has no entitlement policy", t.ID)
	}
	entFile, err := stepExtractEntitlements(r, t)
	if err != nil {
		return fmt.Errorf("extract entitlements: %w", err)
	}
	r.Note("target=%s step 3: augment entitlements (strip=%v required=%v)",
		t.ID, t.Entitlements.Strip, t.Entitlements.RequiredBooleanEntitlements)
	if err := stepMoveToReal(r, t); err != nil {
		return fmt.Errorf("move binary to .real: %w", err)
	}
	if err := stepInstallShim(r, t); err != nil {
		return fmt.Errorf("install shim: %w", err)
	}
	if err := stepPatchBundledComputerUse(r, t); err != nil {
		return fmt.Errorf("patch bundled computer use helper: %w", err)
	}
	if err := stepResign(r, t, entFile); err != nil {
		return fmt.Errorf("re-sign: %w", err)
	}
	stepStripQuarantine(r, t)
	return nil
}

// KeychainMigrate runs only steps 1b + 7a against an already-patched app.
// Useful for retroactive ACL cleanup.
func KeychainMigrate(t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	r := NewRunner(opts.DryRun, opts.Out)
	r.Note("target=%s keychain-migrate: step 1b + 7a only", t.ID)

	if opts.DryRun {
		r.Note("target=%s would capture and re-grant services=%v", t.ID, t.KeychainServices)
		return nil
	}
	items, err := CaptureItems(t)
	if err != nil {
		return fmt.Errorf("keychain capture: %w", err)
	}
	r.Note("target=%s captured %d items", t.ID, len(items))
	if len(items) == 0 {
		return nil
	}
	if err := RegrantItems(t, items); err != nil {
		return fmt.Errorf("keychain re-grant: %w", err)
	}
	r.Note("target=%s re-granted %d items", t.ID, len(items))
	return nil
}

func loadInfoPlistOrPlaceholder(t targets.Target, dryRun bool) (InfoPlist, error) {
	p := paths.InfoPlistPath(t)
	if _, err := os.Stat(p); err != nil {
		if dryRun {
			return InfoPlist{
				CFBundleExecutable: t.ExecName,
				CFBundleVersion:    "0.0.0-dry-run",
				CFBundleIdentifier: t.BundleID,
			}, nil
		}
		return InfoPlist{}, fmt.Errorf("info plist not found at %s: %w", p, err)
	}
	return ReadInfoPlist(p)
}

func stepBackup(r *Runner, t targets.Target) error {
	bundle := paths.BackupBundle(t)
	if !r.DryRun {
		if _, err := os.Stat(bundle); err == nil {
			r.Note("target=%s step 1: backup exists at %s, skipping", t.ID, bundle)
			return nil
		}
		if err := os.MkdirAll(paths.BackupDir(t), 0o755); err != nil {
			return err
		}
	}
	r.Note("target=%s step 1: backup %s -> %s", t.ID, t.AppPath, bundle)
	return r.Run("/usr/bin/rsync", "-a", t.AppPath+"/", bundle+"/")
}

func stepExtractEntitlements(r *Runner, t targets.Target) (string, error) {
	// Prefer .real if it exists (idempotent re-patch path); else read from the
	// main binary slot, which on a fresh patch is the vendor binary.
	source := paths.MainBinaryPath(t)
	if _, err := os.Stat(paths.RealBinaryPath(t)); err == nil {
		source = paths.RealBinaryPath(t)
	}
	if t.Entitlements == nil {
		return "", fmt.Errorf("target %s has no entitlement policy", t.ID)
	}
	return writeAugmentedEntitlementsFile(r, "target="+t.ID+" step 2", source, *t.Entitlements)
}

func stepMoveToReal(r *Runner, t targets.Target) error {
	main := paths.MainBinaryPath(t)
	real_ := paths.RealBinaryPath(t)
	r.Note("target=%s step 4: mv %s -> %s", t.ID, main, real_)
	if r.DryRun {
		return nil
	}
	if _, err := os.Stat(real_); err == nil {
		r.Note("target=%s %s.real already exists, skipping move", t.ID, t.ExecName)
		return nil
	}
	return os.Rename(main, real_)
}

func stepInstallShim(r *Runner, t targets.Target) error {
	main := paths.MainBinaryPath(t)
	r.Note("target=%s step 5: install shim (%d bytes) -> %s", t.ID, len(shimembed.ShimBinary), main)
	if r.DryRun {
		return nil
	}
	if len(shimembed.ShimBinary) == 0 {
		return errors.New("embedded shim is empty; run `make shim` before building")
	}
	if err := os.WriteFile(main, shimembed.ShimBinary, 0o755); err != nil {
		return err
	}
	return os.Chmod(main, 0o755)
}

func stepResign(r *Runner, t targets.Target, entFile string) error {
	id, err := resolveSignIdentity(r.DryRun)
	if err != nil {
		return err
	}
	r.Note("target=%s step 6: re-sign with %q (sha1=%s)", t.ID, paths.SignIdentity, id)
	if err := stepResignNestedCode(r, t, id); err != nil {
		return err
	}
	if err := r.Run("/usr/bin/codesign", codesignRuntimeEntitlementsArgs(id, entFile, paths.RealBinaryPath(t))...); err != nil {
		return fmt.Errorf("sign %s.real: %w", t.ExecName, err)
	}
	if err := r.Run("/usr/bin/codesign", codesignRuntimeEntitlementsArgs(id, entFile, paths.MainBinaryPath(t))...); err != nil {
		return fmt.Errorf("sign %s shim: %w", t.ExecName, err)
	}
	if err := r.Run("/usr/bin/codesign", codesignRuntimeEntitlementsArgs(id, entFile, t.AppPath)...); err != nil {
		return fmt.Errorf("seal outer bundle: %w", err)
	}
	return nil
}

func codesignRuntimeEntitlementsArgs(id string, entFile string, codePath string) []string {
	return []string{
		"--force",
		"--sign",
		id,
		"--options",
		"runtime",
		"--entitlements",
		entFile,
		codePath,
	}
}

func codesignRuntimeArgs(id string, codePath string) []string {
	return []string{
		"--force",
		"--sign",
		id,
		"--options",
		"runtime",
		codePath,
	}
}

func stepResignNestedCode(r *Runner, t targets.Target, id string) error {
	for _, relPath := range t.NestedSignPaths {
		codePath := filepath.Join(t.AppPath, filepath.FromSlash(relPath))
		if !r.DryRun {
			if _, err := os.Stat(codePath); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					r.Note("target=%s step 6: nested code object missing, skipping %s", t.ID, codePath)
					continue
				}
				return fmt.Errorf("stat nested code object %s: %w", codePath, err)
			}
		}
		r.Note("target=%s step 6: re-sign nested code object %s", t.ID, codePath)
		if err := r.Run(
			"/usr/bin/codesign",
			"--force",
			"--sign",
			id,
			"--options",
			"runtime",
			"--preserve-metadata=entitlements",
			codePath,
		); err != nil {
			return fmt.Errorf("sign nested code object %s: %w", codePath, err)
		}
	}
	return nil
}

var identityLineRE = regexp.MustCompile(`^\s*\d+\)\s+([0-9A-F]{40})\s+"([^"]+)"\s*$`)

// resolveSignIdentity returns the SHA-1 hash of the first codesigning identity
// whose common name matches paths.SignIdentity. The keychain may hold multiple
// certs with the same CN (the user's keychain has duplicates for this CN);
// codesign rejects an ambiguous CN, so we resolve to the first matching hash
// up front.
func resolveSignIdentity(dryRun bool) (string, error) {
	if dryRun {
		return paths.SignIdentity, nil
	}
	cmd := exec.Command("/usr/bin/security", "find-identity", "-v", "-p", "codesigning")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("security find-identity: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		m := identityLineRE.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		if m[2] == paths.SignIdentity {
			return m[1], nil
		}
	}
	return "", fmt.Errorf("no codesigning identity matches %q", paths.SignIdentity)
}

func stepStripQuarantine(r *Runner, t targets.Target) {
	r.Note("target=%s step 7: strip com.apple.quarantine (best effort)", t.ID)
	if r.DryRun {
		return
	}
	cmd := exec.Command("/usr/bin/xattr", "-d", "com.apple.quarantine", t.AppPath)
	_ = cmd.Run()
}

func stepWriteState(r *Runner, t targets.Target, version string) error {
	r.Note("target=%s step 8: write state.json version=%s -> %s", t.ID, version, paths.StateFile())
	if r.DryRun {
		return nil
	}
	ms, err := state.Load(paths.StateFile())
	if err != nil {
		return err
	}
	originalDR := ms.Targets[t.ID].OriginalDesignatedRequirement
	if originalDR == "" {
		if captured, captureErr := captureOriginalDR(t); captureErr != nil {
			return fmt.Errorf("capture original DR from backup: %w", captureErr)
		} else {
			originalDR = captured
			r.Note("target=%s captured original DR (%d bytes)", t.ID, len(originalDR))
		}
	}
	ms.Targets[t.ID] = state.TargetState{
		PatchedVersion:                version,
		PatchedAt:                     time.Now().UTC(),
		SignIdentity:                  paths.SignIdentity,
		OriginalDesignatedRequirement: originalDR,
	}
	return state.Save(paths.StateFile(), ms)
}

// EnsureOriginalDesignatedRequirement returns the recorded upstream
// DesignatedRequirement for t. If a state entry exists but predates the
// field, it repairs state.json from the existing backup bundle.
func EnsureOriginalDesignatedRequirement(t targets.Target) (string, error) {
	return OriginalDesignatedRequirement(t, true)
}

// OriginalDesignatedRequirement returns the recorded upstream
// DesignatedRequirement for t. If repair is true and a state entry exists but
// predates the field, it writes the captured DR back to state.json. If repair
// is false, it captures from backup when possible but leaves state.json alone.
func OriginalDesignatedRequirement(t targets.Target, repair bool) (string, error) {
	ms, err := state.Load(paths.StateFile())
	if err != nil {
		return "", fmt.Errorf("load state.json: %w", err)
	}
	entry, ok := ms.Targets[t.ID]
	if !ok {
		return "", fmt.Errorf("no state entry for target %s; run `desktop-via-clyde patch %s` first", t.ID, t.ID)
	}
	if entry.OriginalDesignatedRequirement != "" {
		return entry.OriginalDesignatedRequirement, nil
	}
	captured, err := captureOriginalDR(t)
	if err != nil {
		return "", fmt.Errorf("state entry for target %s has no recorded OriginalDesignatedRequirement and repair from backup failed: %w", t.ID, err)
	}
	if !repair {
		return captured, nil
	}
	entry.OriginalDesignatedRequirement = captured
	ms.Targets[t.ID] = entry
	if err := state.Save(paths.StateFile(), ms); err != nil {
		return "", fmt.Errorf("save repaired state.json: %w", err)
	}
	return captured, nil
}

// captureOriginalDR reads the DesignatedRequirement string from the
// upstream-signed binary inside the backup bundle and returns it as
// a single requirement-language line. The function reads from the
// backup bundle because the live /Applications copy has already been
// re-signed with Goodkind by the time stepWriteState runs; the
// backup created in step 1 is the only on-disk copy of the bundle
// that still carries Anysphere's signature, so the DR captured from
// there reflects what a freshly downloaded update payload from
// upstream must satisfy. Falls back to the live bundle for cases
// where the backup is missing, with a warning logged at the call
// site.
func captureOriginalDR(t targets.Target) (string, error) {
	source := filepath.Join(paths.BackupBundle(t), "Contents", "MacOS", t.ExecName)
	if _, err := os.Stat(source); err != nil {
		realInBackup := filepath.Join(paths.BackupBundle(t), "Contents", "MacOS", t.ExecName+".real")
		if _, err := os.Stat(realInBackup); err == nil {
			source = realInBackup
		} else {
			return "", fmt.Errorf("no upstream-signed binary in backup at %s: %w", paths.BackupBundle(t), err)
		}
	}
	cmd := exec.Command("/usr/bin/codesign", "--display", "--requirements", "-", source)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("codesign --display --requirements -: %w", err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return "", fmt.Errorf("codesign produced empty DR blob for %s", source)
	}
	const designatedPrefix = "designated => "
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, designatedPrefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, designatedPrefix)), nil
		}
	}
	return "", fmt.Errorf("no 'designated =>' line in codesign output for %s", source)
}

func stepInstallLaunchAgent(r *Runner) error {
	plistPath := paths.LaunchAgentPlist()
	loaded := isLaunchAgentLoaded(paths.LaunchAgentLabel)
	plistExists := false
	if _, err := os.Stat(plistPath); err == nil {
		plistExists = true
	}
	r.Note("step 9: LaunchAgent plistExists=%v loaded=%v label=%s", plistExists, loaded, paths.LaunchAgentLabel)
	if plistExists && loaded {
		return nil
	}

	binaryPath, err := selfBinaryPath()
	if err != nil {
		return err
	}
	r.Note("step 9: install LaunchAgent binary=%s log=%s", binaryPath, paths.WatcherLog())
	if r.DryRun {
		return nil
	}
	if err := os.MkdirAll(paths.WatcherLogDir(), 0o755); err != nil {
		return err
	}
	if !plistExists {
		rendered, err := launchagent.Render(launchagent.RenderInput{
			BinaryPath: binaryPath,
			LogPath:    paths.WatcherLog(),
		})
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(plistPath, []byte(rendered), 0o644); err != nil {
			return err
		}
	}
	if !loaded {
		uid := strconv.Itoa(os.Getuid())
		return r.Run("/bin/launchctl", "bootstrap", "gui/"+uid, plistPath)
	}
	return nil
}

// isLaunchAgentLoaded checks `launchctl print gui/<uid>/<label>` exit code.
func isLaunchAgentLoaded(label string) bool {
	uid := strconv.Itoa(os.Getuid())
	cmd := exec.Command("/bin/launchctl", "print", "gui/"+uid+"/"+label)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func stepVerify(r *Runner, t targets.Target) error {
	r.Note("target=%s step 10: verify bundle signature and shim dry-run", t.ID)
	if r.DryRun {
		return nil
	}
	for _, relPath := range t.NestedSignPaths {
		codePath := filepath.Join(t.AppPath, filepath.FromSlash(relPath))
		if _, err := os.Stat(codePath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("stat nested code object %s: %w", codePath, err)
		}
		if err := r.Run("/usr/bin/codesign", "--verify", "--verbose=2", codePath); err != nil {
			return fmt.Errorf("verify nested code object %s: %w", codePath, err)
		}
	}
	if err := r.Run("/usr/bin/codesign", "--verify", "--verbose=2", t.AppPath); err != nil {
		return err
	}
	if err := verifyRequiredEntitlements(r, t); err != nil {
		return err
	}
	out, err := r.RunCaptureStdout(paths.MainBinaryPath(t), "--clyde-dry-run")
	if err != nil {
		return fmt.Errorf("shim dry-run: %w", err)
	}
	r.Note("target=%s shim dry-run output:\n%s", t.ID, string(out))
	return nil
}

func selfBinaryPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p, nil
	}
	return resolved, nil
}
