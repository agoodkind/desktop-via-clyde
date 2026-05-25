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
	"time"
)

var stateLog = slog.With("component", "desktop-via-clyde", "subcomponent", "state")

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

// Save writes state.json atomically (write to sibling tmp, rename).
func Save(path string, s MultiState) error {
	stateLog.Debug("state.save", "path", path)
	if s.Targets == nil {
		s.Targets = map[string]TargetState{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		stateLog.Error("state.save.mkdir_failed", "path", path, "err", err)
		return fmt.Errorf("create state dir for %s: %w", path, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		stateLog.Error("state.save.encode_failed", "path", path, "err", err)
		return fmt.Errorf("encode state for %s: %w", path, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		stateLog.Error("state.save.write_failed", "path", tmp, "err", err)
		return fmt.Errorf("write temp state file %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		stateLog.Error("state.save.rename_failed", "from", tmp, "to", path, "err", err)
		return fmt.Errorf("rename temp state file %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// Remove deletes the state file. Missing files are not an error.
func Remove(path string) error {
	stateLog.Debug("state.remove", "path", path)
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		stateLog.Error("state.remove.failed", "path", path, "err", err)
		return fmt.Errorf("remove state file %s: %w", path, err)
	}
	return nil
}
