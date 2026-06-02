package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestUpdateConcurrentWritesPreserveTargets(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	targetIDs := []string{"alpha", "beta", "gamma", "delta"}

	var waitGroup sync.WaitGroup
	start := make(chan struct{})
	for _, targetID := range targetIDs {
		waitGroup.Add(1)
		go func(targetID string) {
			defer waitGroup.Done()
			<-start
			err := Update(statePath, func(ms MultiState) (MultiState, error) {
				ms.Targets[targetID] = TargetState{PatchedVersion: targetID, PatchedAt: time.Unix(0, 0).UTC()}
				return ms, nil
			})
			if err != nil {
				t.Errorf("Update(%s): %v", targetID, err)
			}
		}(targetID)
	}
	close(start)
	waitGroup.Wait()

	loaded, err := Load(statePath)
	if err != nil {
		t.Fatalf("Load(%s): %v", statePath, err)
	}
	if len(loaded.Targets) != len(targetIDs) {
		t.Fatalf("target count = %d, want %d", len(loaded.Targets), len(targetIDs))
	}
	for _, targetID := range targetIDs {
		if loaded.Targets[targetID].PatchedVersion != targetID {
			t.Fatalf("state for %s = %#v", targetID, loaded.Targets[targetID])
		}
	}
}

func TestUpdateRemovesStateFileWhenTargetsBecomeEmpty(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := Update(statePath, func(ms MultiState) (MultiState, error) {
		ms.Targets["alpha"] = TargetState{PatchedVersion: "1", PatchedAt: time.Unix(0, 0).UTC()}
		return ms, nil
	}); err != nil {
		t.Fatalf("Update(%s): %v", statePath, err)
	}
	if err := Update(statePath, func(ms MultiState) (MultiState, error) {
		delete(ms.Targets, "alpha")
		return ms, nil
	}); err != nil {
		t.Fatalf("Update(%s): %v", statePath, err)
	}
	if _, err := os.Stat(statePath); !os.IsNotExist(err) {
		t.Fatalf("state file stat err = %v, want not exists", err)
	}
}
