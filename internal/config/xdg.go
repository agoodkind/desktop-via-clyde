package config

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	configAppName  = "desktop-via-clyde"
	storageAppName = "clyde"
)

// Path returns the XDG-resolved desktop-via-clyde config.toml path.
func Path() string {
	return filepath.Join(configRoot(), "config.toml")
}

// StateRoot returns the XDG-resolved Clyde state root shared by the harness.
func StateRoot() string {
	return stateRoot()
}

// CacheRoot returns the XDG-resolved Clyde cache root shared by the harness.
func CacheRoot() string {
	return cacheRoot()
}

func configRoot() string {
	return appScopedRoot(configAppName, "XDG_CONFIG_HOME", ".config")
}

func stateRoot() string {
	return appScopedRoot(storageAppName, "XDG_STATE_HOME", filepath.Join(".local", "state"))
}

func cacheRoot() string {
	return appScopedRoot(storageAppName, "XDG_CACHE_HOME", ".cache")
}

func appScopedRoot(appName string, envVar string, fallbackRelative string) string {
	if base, ok := xdgBaseFromEnv(envVar); ok {
		return filepath.Join(base, appName)
	}
	return filepath.Join(homeRelativeRoot(fallbackRelative), appName)
}

func xdgBaseFromEnv(envVar string) (string, bool) {
	value := strings.TrimSpace(os.Getenv(envVar))
	if value == "" {
		return "", false
	}
	return cleanExpandedPath(value), true
}

func homeRelativeRoot(relativePath string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return cleanExpandedPath(filepath.Join(home, relativePath))
}

func cleanExpandedPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(expandLeadingHome(path))
}

func expandLeadingHome(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
