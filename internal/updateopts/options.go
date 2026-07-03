// Package updateopts adapts desktop-via-clyde build metadata to selfupdate.
package updateopts

import (
	"log/slog"
	"net/http"

	"goodkind.io/desktop-via-clyde/internal/version"
	"goodkind.io/go-makefile/selfupdate"
)

const (
	updateRepo = "agoodkind/desktop-via-clyde"
	// BinaryName is the release archive and installed binary name; callers
	// that derive library paths (state, cache) key them by this same name.
	BinaryName = "desktop-via-clyde"
)

// Overrides carries operation-specific update settings.
type Overrides struct {
	Client      *http.Client
	InstallPath string
	DryRun      bool
	Log         *slog.Logger
}

// Options builds selfupdate options while keeping library default paths.
func Options(overrides Overrides) selfupdate.Options {
	return selfupdate.Options{
		Config: selfupdate.Config{
			Repo:              updateRepo,
			Binary:            BinaryName,
			CurrentVersion:    version.Version,
			CurrentCommit:     version.Commit,
			CurrentBuildHash:  version.BuildHash(),
			AllowPrerelease:   nil,
			SignerWorkflowURI: "",
			APIBaseURLEnv:     "",
		},
		Client:      overrides.Client,
		InstallPath: overrides.InstallPath,
		CacheDir:    "",
		StatePath:   "",
		DryRun:      overrides.DryRun,
		Log:         overrides.Log,
	}
}
