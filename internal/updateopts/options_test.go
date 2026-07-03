package updateopts

import (
	"testing"

	"goodkind.io/desktop-via-clyde/internal/version"
	gklogversion "goodkind.io/gklog/version"
)

func TestOptionsUseDesktopViaClydeReleaseDefaults(t *testing.T) {
	options := Options(Overrides{})

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
	if options.Config.CurrentBuildHash != gklogversion.BinHash {
		t.Fatalf("CurrentBuildHash = %q, want %q", options.Config.CurrentBuildHash, gklogversion.BinHash)
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
}
