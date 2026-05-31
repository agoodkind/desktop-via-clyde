package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRequiredMissingFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	_, err := LoadRequired()
	if err == nil {
		t.Fatal("expected missing config error")
	}
}

func TestLoadRequiredResolvesDesktopViaClydeXDGPath(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	writeConfigForTest(t, configHome, "[apps.cursor.updater]\ndefault_channel = \"stable\"\n")

	cfg, err := LoadRequired()
	if err != nil {
		t.Fatalf("LoadRequired: %v", err)
	}
	if cfg.Apps.Cursor.Updater.DefaultChannel != "stable" {
		t.Fatalf("cursor default channel = %q", cfg.Apps.Cursor.Updater.DefaultChannel)
	}
}

func TestLoadRequiredRejectsInvalidTargetPolicy(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	writeConfigForTest(t, configHome, "[apps.cursor]\ntarget_policy = \"invalid\"\n")

	_, err := LoadRequired()
	if err == nil {
		t.Fatal("expected invalid target policy error")
	}
}

func TestLoadRequiredLoadsChannelForms(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	writeConfigForTest(t, configHome, `[apps.cursor.updater]
channels = ["stable", "dev"]
default_channel = "dev"

[apps.codex.updater]
default_channel = "stable"

[[apps.codex.updater.channels]]
name = "stable"
url = "https://example.com/stable.xml"

[[apps.codex.updater.channels]]
name = "beta"
url = "https://example.com/beta.xml"
`)

	cfg, err := LoadRequired()
	if err != nil {
		t.Fatalf("LoadRequired: %v", err)
	}
	if len(cfg.Apps.Cursor.Updater.Channels) != 2 {
		t.Fatalf("cursor channels = %d", len(cfg.Apps.Cursor.Updater.Channels))
	}
	if cfg.Apps.Cursor.Updater.Channels[1].Name != "dev" {
		t.Fatalf("cursor channel[1] = %q", cfg.Apps.Cursor.Updater.Channels[1].Name)
	}
	if len(cfg.Apps.Codex.Updater.Channels) != 2 {
		t.Fatalf("codex channels = %d", len(cfg.Apps.Codex.Updater.Channels))
	}
	if cfg.Apps.Codex.Updater.Channels[0].URL != "https://example.com/stable.xml" {
		t.Fatalf("codex stable URL = %q", cfg.Apps.Codex.Updater.Channels[0].URL)
	}
}

func writeConfigForTest(t *testing.T, xdgConfigHome string, body string) {
	t.Helper()

	root := filepath.Join(xdgConfigHome, "desktop-via-clyde")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
