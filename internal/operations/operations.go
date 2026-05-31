// Package operations dispatches configured command capabilities onto the
// concrete app and CLI behavior linked into this binary.
package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/clihandlers"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"goodkind.io/desktop-via-clyde/internal/upgrade"
)

// FlagValues holds parsed command flag values keyed by flag name.
type FlagValues struct {
	strings map[string]string
	bools   map[string]bool
}

// NewFlagValues builds an empty FlagValues container.
func NewFlagValues() FlagValues {
	return FlagValues{
		strings: map[string]string{},
		bools:   map[string]bool{},
	}
}

// SetString stores one string flag value.
func (f FlagValues) SetString(name string, value string) {
	f.strings[name] = value
}

// SetBool stores one bool flag value.
func (f FlagValues) SetBool(name string, value bool) {
	f.bools[name] = value
}

// String returns one stored string flag value.
func (f FlagValues) String(name string) string {
	return f.strings[name]
}

// Bool returns one stored bool flag value.
func (f FlagValues) Bool(name string) bool {
	return f.bools[name]
}

// Request describes one command dispatch into a typed capability.
type Request struct {
	Out        io.Writer
	App        *targets.Target
	CLI        *targets.CLIProgram
	Capability string
	Flags      FlagValues
}

// Run dispatches one operation capability with parsed flags and an optional
// app or CLI declaration.
func Run(ctx context.Context, req Request) error {
	switch req.Capability {
	case catalog.OperationAppPatch:
		if req.App == nil {
			return fmt.Errorf("%s requires an app target", req.Capability)
		}
		if err := patch.Patch(ctx, *req.App, patch.Options{
			DryRun:            req.Flags.Bool("dry-run"),
			NoMigrateKeychain: req.Flags.Bool("no-migrate-keychain"),
			Out:               req.Out,
			Trace:             nil,
		}); err != nil {
			return operationError(ctx, "operations.patch_failed", "patch app", err)
		}
		return nil
	case catalog.OperationAppUnpatch:
		if req.App == nil {
			return fmt.Errorf("%s requires an app target", req.Capability)
		}
		if err := patch.Unpatch(ctx, *req.App, patch.Options{
			DryRun:            req.Flags.Bool("dry-run"),
			NoMigrateKeychain: false,
			Out:               req.Out,
			Trace:             nil,
		}); err != nil {
			return operationError(ctx, "operations.unpatch_failed", "restore app bundle", err)
		}
		return nil
	case catalog.OperationAppUpgrade:
		if req.App == nil {
			return fmt.Errorf("%s requires an app target", req.Capability)
		}
		if err := upgrade.Run(ctx, *req.App, upgrade.Options{
			Channel:           req.Flags.String("channel"),
			DryRun:            req.Flags.Bool("dry-run"),
			NoMigrateKeychain: req.Flags.Bool("no-migrate-keychain"),
			Out:               req.Out,
		}); err != nil {
			return operationError(ctx, "operations.upgrade_failed", "upgrade app", err)
		}
		return nil
	case catalog.OperationAppKeychainMigrate:
		if req.App == nil {
			return fmt.Errorf("%s requires an app target", req.Capability)
		}
		if err := patch.KeychainMigrate(ctx, *req.App, patch.Options{
			DryRun:            req.Flags.Bool("dry-run"),
			NoMigrateKeychain: false,
			Out:               req.Out,
			Trace:             nil,
		}); err != nil {
			return operationError(ctx, "operations.keychain_migrate_failed", "restore keychain access", err)
		}
		return nil
	case catalog.OperationAppStatus:
		if req.App == nil {
			return fmt.Errorf("%s requires an app target", req.Capability)
		}
		if err := writeAppStatus(ctx, req.Out, *req.App); err != nil {
			return operationError(ctx, "operations.app_status_failed", "print app status", err)
		}
		return nil
	case catalog.OperationStandaloneInstall:
		if err := clihandlers.InstallStandaloneCLI(ctx, clihandlers.Request{
			Flags: req.Flags,
			Out:   req.Out,
		}); err != nil {
			return operationError(ctx, "operations.standalone_install_failed", "install standalone cli", err)
		}
		return nil
	case catalog.OperationStandaloneStatus:
		if err := clihandlers.StatusStandaloneCLI(ctx, clihandlers.Request{
			Flags: req.Flags,
			Out:   req.Out,
		}); err != nil {
			return operationError(ctx, "operations.standalone_status_failed", "print standalone cli status", err)
		}
		return nil
	default:
		return fmt.Errorf("unknown operation capability %q", req.Capability)
	}
}

func writeAppStatus(ctx context.Context, out io.Writer, target targets.Target) error {
	multiState, err := state.Load(paths.StateFile())
	if err != nil {
		return operationError(ctx, "operations.app_status_load_state_failed", "load state file "+paths.StateFile(), err)
	}
	fmt.Fprintf(out, "state file: %s\n", paths.StateFile())
	fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", "TARGET", "STATE", "VERSION", "NOTES")

	entry, hasState := multiState.Targets[target.ID]
	appExists, appStatErr := pathExists(ctx, target.AppPath)
	if appStatErr != nil {
		return operationError(ctx, "operations.app_status_stat_app_failed", "stat app bundle "+target.AppPath, appStatErr)
	}
	if !appExists {
		fmt.Fprintf(out, "%-8s  %-9s  %-20s  bundle missing at %s\n", target.ID, "absent", "-", target.AppPath)
		return nil
	}
	if !hasState {
		fmt.Fprintf(out, "%-8s  %-9s  %-20s  bundle present, no state entry\n", target.ID, "clean", "-")
		return nil
	}

	currentVersion := readBundleVersion(target)
	stateLabel := "patched"
	notes := fmt.Sprintf("signed-as=%q", entry.SignIdentity)
	realPathExists, realPathErr := pathExists(ctx, paths.RealBinaryPath(target))
	if realPathErr != nil {
		return operationError(ctx, "operations.app_status_stat_real_failed", "stat restored binary path "+paths.RealBinaryPath(target), realPathErr)
	}
	if !realPathExists {
		stateLabel = "drifted"
		notes = notes + "; " + target.ExecName + ".real missing"
	} else if currentVersion != "" && currentVersion != entry.PatchedVersion {
		stateLabel = "drifted"
		notes += fmt.Sprintf("; current version %s != patched %s", currentVersion, entry.PatchedVersion)
	}
	fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", target.ID, stateLabel, entry.PatchedVersion, notes)
	return nil
}

func operationError(ctx context.Context, event string, message string, err error) error {
	slog.Default().ErrorContext(ctx, event, "err", err)
	return errors.New(message + ": " + err.Error())
}

func pathExists(ctx context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	slog.Default().ErrorContext(ctx, "operations.path_exists_stat_failed", "path", path, "err", err)
	return false, errors.New("stat " + path + ": " + err.Error())
}

func readBundleVersion(target targets.Target) string {
	info, err := patch.ReadInfoPlist(paths.InfoPlistPath(target))
	if err != nil {
		return ""
	}
	return info.CFBundleVersion
}
