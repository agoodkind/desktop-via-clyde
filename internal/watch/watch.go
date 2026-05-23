// Package watch implements the FSEvents watcher used by the LaunchAgent.
// It watches every registered target's AppPath that has a state entry, and
// re-applies Patch on drift (3-second debounce per target).
package watch

import (
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

const debounce = 3 * time.Second

// Run is the entry point invoked by `desktop-via-clyde watch`. It blocks
// until the process is signaled.
func Run(logOut io.Writer) error {
	if logOut == nil {
		logOut = os.Stdout
	}
	logger := log.New(logOut, "desktop-via-clyde-watcher ", log.LstdFlags|log.Lmicroseconds)

	ms, err := state.Load(paths.StateFile())
	if err != nil {
		return fmt.Errorf("load state: %w", err)
	}
	if len(ms.Targets) == 0 {
		logger.Printf("no targets in state.json; watcher idle (will keep-alive until state appears or process restarts)")
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("fsnotify.NewWatcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()

	// pathToTarget maps the watched filesystem path back to the target ID.
	pathToTarget := map[string]string{}
	for id := range ms.Targets {
		t, lookupErr := targets.Lookup(id)
		if lookupErr != nil {
			logger.Printf("state.json references unknown target %q; ignoring", id)
			continue
		}
		if err := watcher.Add(t.AppPath); err != nil {
			logger.Printf("watcher.Add(%s) failed: %v; falling back to parent dir", t.AppPath, err)
			if err := watcher.Add(filepath.Dir(t.AppPath)); err != nil {
				logger.Printf("watcher.Add fallback failed for %s: %v", t.AppPath, err)
				continue
			}
			pathToTarget[filepath.Dir(t.AppPath)] = id
		} else {
			pathToTarget[t.AppPath] = id
		}
		logger.Printf("watching %s for target=%s", t.AppPath, t.ID)
	}

	var mu sync.Mutex
	pendingTimers := map[string]*time.Timer{}

	for {
		select {
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			id := matchTargetID(pathToTarget, ev.Name)
			logger.Printf("event: %s (target=%s)", ev.String(), id)
			if id == "" {
				continue
			}
			mu.Lock()
			if t, ok := pendingTimers[id]; ok {
				t.Reset(debounce)
			} else {
				targetID := id
				pendingTimers[id] = time.AfterFunc(debounce, func() {
					mu.Lock()
					delete(pendingTimers, targetID)
					mu.Unlock()
					handleDebounced(logger, targetID)
				})
			}
			mu.Unlock()
		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			logger.Printf("watch error: %v", err)
		}
	}
}

func matchTargetID(pathToTarget map[string]string, eventPath string) string {
	// Exact match first, then a directory-prefix match for the parent-dir
	// fallback case.
	if id, ok := pathToTarget[eventPath]; ok {
		return id
	}
	for watched, id := range pathToTarget {
		if eventPath == watched {
			return id
		}
		if len(eventPath) > len(watched) && eventPath[:len(watched)+1] == watched+"/" {
			return id
		}
	}
	return ""
}

func handleDebounced(logger *log.Logger, targetID string) {
	t, err := targets.Lookup(targetID)
	if err != nil {
		logger.Printf("debounce: unknown target=%s: %v", targetID, err)
		return
	}
	drift, why, err := detectDrift(t)
	if err != nil {
		logger.Printf("target=%s drift check failed: %v", targetID, err)
		return
	}
	if !drift {
		logger.Printf("target=%s no drift detected", targetID)
		return
	}
	logger.Printf("target=%s drift detected: %s", targetID, why)
	notify(logger, fmt.Sprintf("%s updated; re-applying clyde MITM.", filepath.Base(t.AppPath)))

	err = patch.Patch(t, patch.Options{DryRun: false, Out: logger.Writer()})
	if err != nil {
		logger.Printf("target=%s re-patch failed: %v", targetID, err)
		return
	}
	logger.Printf("target=%s re-patch complete", targetID)
}

func detectDrift(t targets.Target) (bool, string, error) {
	ms, err := state.Load(paths.StateFile())
	if err != nil {
		return false, "", err
	}
	cur, ok := ms.Targets[t.ID]
	if !ok {
		return true, "state.json missing target entry", nil
	}
	if _, err := os.Stat(paths.RealBinaryPath(t)); err != nil {
		if os.IsNotExist(err) {
			return true, t.ExecName + ".real missing", nil
		}
		return false, "", err
	}
	out, err := exec.Command("/usr/bin/defaults", "read", t.AppPath+"/Contents/Info", "CFBundleVersion").Output()
	if err != nil {
		return false, "", fmt.Errorf("defaults read CFBundleVersion: %w", err)
	}
	version := stripNewline(string(out))
	if version != cur.PatchedVersion {
		return true, fmt.Sprintf("CFBundleVersion %q != patched %q", version, cur.PatchedVersion), nil
	}
	return false, "", nil
}

func stripNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	return s
}

func notify(logger *log.Logger, message string) {
	script := fmt.Sprintf(`display notification %q with title "desktop-via-clyde"`, message)
	cmd := exec.Command("/usr/bin/osascript", "-e", script)
	if err := cmd.Run(); err != nil {
		logger.Printf("osascript notify failed: %v", err)
	}
}
