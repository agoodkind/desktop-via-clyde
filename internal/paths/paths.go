// Package paths centralizes filesystem locations used by desktop-via-clyde.
package paths

import (
	"os"
	"path/filepath"

	"goodkind.io/desktop-via-clyde/internal/config"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// Home returns the current user's home directory when it is available.
func Home() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// SignIdentity returns the configured local codesigning identity common name.
func SignIdentity() string {
	return config.Current().Signing.Identity
}

// SignTeamID returns the configured local Apple team identifier.
func SignTeamID() string {
	return config.Current().Signing.TeamID
}

// StateRoot returns the XDG-derived Clyde state root used by the harness.
func StateRoot() string {
	return config.StateRoot()
}

// LogDir returns the structured log directory for desktop-via-clyde.
func LogDir() string {
	return filepath.Join(StateRoot(), "logs")
}

// ProcessLogPath returns the main CLI JSONL log path.
func ProcessLogPath() string {
	return filepath.Join(LogDir(), "desktop-via-clyde.jsonl")
}

// StdioTeeLogDir returns the stdio tee log directory under the Clyde state root.
func StdioTeeLogDir() string {
	return filepath.Join(LogDir(), "stdio-tee")
}

// BackupRoot returns the per-target backup root.
func BackupRoot() string {
	return filepath.Join(StateRoot(), "desktop-via-clyde-backup")
}

// BackupDir returns the backup directory for one target ID.
func BackupDir(t targets.Target) string {
	return filepath.Join(BackupRoot(), t.ID)
}

// BackupBundle returns the backup bundle path for one target.
func BackupBundle(t targets.Target) string {
	return filepath.Join(BackupDir(t), filepath.Base(t.AppPath))
}

// StateFile returns the shared patch state file path.
func StateFile() string {
	return filepath.Join(StateRoot(), "desktop-via-clyde-state.json")
}

// MacOSDir returns the bundle MacOS directory for one target.
func MacOSDir(t targets.Target) string {
	return filepath.Join(t.AppPath, "Contents", "MacOS")
}

// MainBinaryPath returns the primary executable path for one target.
func MainBinaryPath(t targets.Target) string {
	return filepath.Join(MacOSDir(t), t.ExecName)
}

// ResourcesDir returns the bundle Resources directory for one target.
func ResourcesDir(t targets.Target) string {
	return filepath.Join(t.AppPath, "Contents", "Resources")
}

// LaunchPolicyPath returns the installed launch policy path for one target. The
// policy lives in Contents/Resources, not Contents/MacOS: codesign seals files
// in Resources as ordinary resources, while a non-Mach-O file beside the
// executable in MacOS breaks the bundle signature.
func LaunchPolicyPath(t targets.Target) string {
	return filepath.Join(ResourcesDir(t), t.ExecName+".launch-policy.json")
}

// RealBinaryPath returns the moved original executable path for one target.
func RealBinaryPath(t targets.Target) string {
	return filepath.Join(MacOSDir(t), t.ExecName+".real")
}

// InfoPlistPath returns the Info.plist path for one target.
func InfoPlistPath(t targets.Target) string {
	return filepath.Join(t.AppPath, "Contents", "Info.plist")
}
