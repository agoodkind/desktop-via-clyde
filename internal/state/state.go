// Package state persists the per-target patched state recorded by the most
// recent patch.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	stateLog = slog.With("component", "desktop-via-clyde", "subcomponent", "state")
	stateMu  sync.Mutex
)

// TargetState is the on-disk record for a single patched bundle.
type TargetState struct {
	PatchedVersion string    `json:"patched_version"`
	PatchedAt      time.Time `json:"patched_at"`
	SignIdentity   string    `json:"sign_identity"`
	// OriginalDesignatedRequirement captures the DesignatedRequirement
	// string from the un-patched upstream-signed bundle, read once at
	// initial patch time via `codesign --display --requirements -`.
	// The upgrade flow uses this string to verify that a freshly
	// downloaded update payload still carries an upstream signature
	// that satisfies the recorded DR before desktop-via-clyde
	// re-signs it locally. Empty when the state entry predates the
	// field, which is the cue to capture it opportunistically on the
	// next patch run.
	OriginalDesignatedRequirement string `json:"original_designated_requirement,omitempty"`
}

// MultiState is the top-level state file shape, keyed by Target.ID.
type MultiState struct {
	Targets map[string]TargetState `json:"targets"`
}

// Load reads state.json from path. A missing file returns an empty MultiState
// with a non-nil Targets map, not an error, because the tool's first run has
// no state on disk yet.
func Load(path string) (MultiState, error) {
	stateLog.Debug("state.load", "path", path)
	return loadUnlocked(path)
}

// Update serializes one load, mutate, save cycle against the state file.
func Update(path string, mutate func(MultiState) (MultiState, error)) error {
	if mutate == nil {
		return fmt.Errorf("state update callback is required")
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	stateLog.Debug("state.update", "path", path)
	current, err := loadUnlocked(path)
	if err != nil {
		return err
	}
	updated, err := mutate(current)
	if err != nil {
		return err
	}
	if len(updated.Targets) == 0 {
		return removeUnlocked(path)
	}
	return saveUnlocked(path, updated)
}

func loadUnlocked(path string) (MultiState, error) {
	s := MultiState{Targets: map[string]TargetState{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		stateLog.Error("state.load.read_failed", "path", path, "err", err)
		return s, fmt.Errorf("read state file %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &s); err != nil {
		stateLog.Error("state.load.parse_failed", "path", path, "err", err)
		return s, fmt.Errorf("parse state file %s: %w", path, err)
	}
	if s.Targets == nil {
		s.Targets = map[string]TargetState{}
	}
	return s, nil
}

func saveUnlocked(path string, s MultiState) error {
	if s.Targets == nil {
		s.Targets = map[string]TargetState{}
	}
	dirPath := filepath.Dir(path)
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		stateLog.Error("state.save.mkdir_failed", "path", path, "err", err)
		return fmt.Errorf("create state dir for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		stateLog.Error("state.save.encode_failed", "path", path, "err", err)
		return fmt.Errorf("encode state for %s: %w", path, err)
	}
	tmpFile, err := os.CreateTemp(dirPath, filepath.Base(path)+".tmp-*")
	if err != nil {
		stateLog.Error("state.save.create_temp_failed", "path", path, "err", err)
		return fmt.Errorf("create temp state file for %s: %w", path, err)
	}
	tmpPath := tmpFile.Name()
	if _, err := tmpFile.Write(data); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		stateLog.Error("state.save.write_failed", "path", tmpPath, "err", err)
		return fmt.Errorf("write temp state file %s: %w", tmpPath, err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		stateLog.Error("state.save.close_failed", "path", tmpPath, "err", err)
		return fmt.Errorf("close temp state file %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		stateLog.Error("state.save.chmod_failed", "path", tmpPath, "err", err)
		return fmt.Errorf("chmod temp state file %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		stateLog.Error("state.save.rename_failed", "from", tmpPath, "to", path, "err", err)
		return fmt.Errorf("rename temp state file %s -> %s: %w", tmpPath, path, err)
	}
	return nil
}

func removeUnlocked(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		stateLog.Error("state.remove.failed", "path", path, "err", err)
		return fmt.Errorf("remove state file %s: %w", path, err)
	}
	return nil
}
