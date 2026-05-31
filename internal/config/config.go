// Package config loads and stores the active desktop-via-clyde configuration.
package config

import (
	"fmt"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/spec"
)

var (
	currentMu     sync.RWMutex
	currentConfig *spec.Config
)

// Current returns the active runtime config. The command entrypoint must load
// and install config before any code path relies on it.
func Current() *spec.Config {
	currentMu.RLock()
	defer currentMu.RUnlock()
	if currentConfig == nil {
		return &spec.Config{
			Signing: spec.SigningSpec{
				Identity: "",
				TeamID:   "",
			},
			Apps: map[string]spec.AppSpec{},
			CLIs: map[string]spec.CLISpec{},
		}
	}
	return currentConfig.Clone()
}

// SetCurrent installs the active runtime config for the current process.
func SetCurrent(cfg *spec.Config) {
	currentMu.Lock()
	defer currentMu.Unlock()
	if cfg == nil {
		currentConfig = nil
		return
	}
	currentConfig = cfg.Clone()
}

// LoadRequired reads and validates the required XDG config file.
func LoadRequired() (*spec.Config, error) {
	cfg, err := LoadPath(Path())
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("config %s did not produce a runtime config", Path())
	}
	return cfg, nil
}
