package updateopts

import (
	"testing"

	"goodkind.io/desktop-via-clyde/internal/version"
	gklogversion "goodkind.io/gklog/version"
)

func TestOptionsUseDesktopViaClydeReleaseDefaults(t *testing.T) {
	originalBinHash := gklogversion.BinHash
	t.Cleanup(func() {
		gklogversion.BinHash = originalBinHash
	})
	gklogversion.BinHash = ""

	options := Options(emptyOverrides())

	if options.Config.Repo != "agoodkind/desktop-via-clyde" {
		t.Fatalf("Repo = %q, want agoodkind/desktop-via-clyde", options.Config.Repo)
	}
	if options.Config.Binary != "desktop-via-clyde" {
		t.Fatalf("Binary = %q, want desktop-via-clyde", options.Config.Binary)
	}
	if options.Config.CurrentVersion != version.Version {
		t.Fatalf("CurrentVersion = %q, want %q", options.Config.CurrentVersion, version.Version)
	}
	if options.Config.CurrentCommit != version.Commit {
		t.Fatalf("CurrentCommit = %q, want %q", options.Config.CurrentCommit, version.Commit)
	}
	if options.Config.CurrentBuildHash == "" || options.Config.CurrentBuildHash == "unknown" {
		t.Fatalf("CurrentBuildHash = %q, want runtime hash fallback", options.Config.CurrentBuildHash)
	}
	if options.Config.AllowPrerelease != nil {
		t.Fatalf("AllowPrerelease = %#v, want nil", options.Config.AllowPrerelease)
	}
	if options.CacheDir != "" {
		t.Fatalf("CacheDir = %q, want library default", options.CacheDir)
	}
	if options.StatePath != "" {
		t.Fatalf("StatePath = %q, want library default", options.StatePath)
	}

	gklogversion.BinHash = "stampedhash"
	stampedOptions := Options(emptyOverrides())
	if stampedOptions.Config.CurrentBuildHash != "stampedhash" {
		t.Fatalf("CurrentBuildHash = %q, want stampedhash", stampedOptions.Config.CurrentBuildHash)
	}
}

func emptyOverrides() Overrides {
	return Overrides{
		Client:      nil,
		InstallPath: "",
		DryRun:      false,
		Log:         nil,
	}
}
