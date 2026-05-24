// Package state persists the per-target patched state recorded by the most
// recent patch.
package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
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
	s := MultiState{Targets: map[string]TargetState{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, err
	}
	if s.Targets == nil {
		s.Targets = map[string]TargetState{}
	}
	return s, nil
}

// Save writes state.json atomically (write to sibling tmp, rename).
func Save(path string, s MultiState) error {
	if s.Targets == nil {
		s.Targets = map[string]TargetState{}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Remove deletes the state file. Missing files are not an error.
func Remove(path string) error {
	err := os.Remove(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
