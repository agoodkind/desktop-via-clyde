// Package paths centralizes filesystem locations used by desktop-via-clyde.
// All paths are per-target unless they are global state or CA certificate
// references.
package paths

import (
	"os"
	"path/filepath"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

// SignIdentity is the user's stable Developer ID. We resolve it to a SHA-1
// hash at sign time to disambiguate between duplicate keychain entries.
const SignIdentity = "Developer ID Application: Alex Goodkind (H3BMXM4W7H)"

// SignTeamID is the Apple team identifier for SignIdentity.
const SignTeamID = "H3BMXM4W7H"

// StateRootEnv overrides the Application Support state root for isolated
// upgrade smokes against copied app bundles.
const StateRootEnv = "DESKTOP_VIA_CLYDE_STATE_ROOT"

// Home returns the user's home directory, or an empty string if unavailable.
func Home() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// StateRoot is the Application Support directory for the tool.
func StateRoot() string {
	if override := os.Getenv(StateRootEnv); override != "" {
		return override
	}
	return filepath.Join(Home(), "Library", "Application Support", "desktop-via-clyde")
}

// BackupRoot holds per-target backup bundles.
func BackupRoot() string {
	return filepath.Join(StateRoot(), "backup")
}

// BackupDir returns the per-target directory under backup/.
func BackupDir(t targets.Target) string {
	return filepath.Join(BackupRoot(), t.ID)
}

// BackupBundle returns the absolute path of the backed-up .app for target t.
func BackupBundle(t targets.Target) string {
	return filepath.Join(BackupDir(t), filepath.Base(t.AppPath))
}

// StateFile is the shared state.json that records every patched target.
func StateFile() string {
	return filepath.Join(StateRoot(), "state.json")
}

// MacOSDir is <App>/Contents/MacOS for target t.
func MacOSDir(t targets.Target) string {
	return filepath.Join(t.AppPath, "Contents", "MacOS")
}

// MainBinaryPath is <App>/Contents/MacOS/<ExecName> (the shim slot).
func MainBinaryPath(t targets.Target) string {
	return filepath.Join(MacOSDir(t), t.ExecName)
}

// RealBinaryPath is <App>/Contents/MacOS/<ExecName>.real (the moved original).
func RealBinaryPath(t targets.Target) string {
	return filepath.Join(MacOSDir(t), t.ExecName+".real")
}

// InfoPlistPath is <App>/Contents/Info.plist.
func InfoPlistPath(t targets.Target) string {
	return filepath.Join(t.AppPath, "Contents", "Info.plist")
}

// ClydeCAPath is the read-only reference to the MITM CA the shim relies on.
func ClydeCAPath() string {
	return filepath.Join(Home(), ".local", "state", "clyde", "mitm", "ca", "clyde-mitm-ca.crt")
}
