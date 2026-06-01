// Package helperdispatch lets the desktop-via-clyde monolith run registered
// helper entrypoints from copied executable paths.
package helperdispatch

import (
	"fmt"
	"sort"
	"sync"
)

// Handler runs when the current executable invocation matches a helper mode.
type Handler func() (int, bool)

var (
	mu       sync.RWMutex
	handlers = map[string]Handler{}
)

// Register links one helper entrypoint compiled into the monolith.
func Register(name string, handler Handler) error {
	if name == "" {
		return fmt.Errorf("helper name is required")
	}
	if handler == nil {
		return fmt.Errorf("helper %q handler is required", name)
	}
	mu.Lock()
	defer mu.Unlock()
	if _, ok := handlers[name]; ok {
		return fmt.Errorf("helper %q handler is already registered", name)
	}
	handlers[name] = handler
	return nil
}

// RunIfMatched executes the first helper handler that claims this invocation.
func RunIfMatched() (int, bool) {
	mu.RLock()
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	snapshot := make([]Handler, 0, len(names))
	for _, name := range names {
		snapshot = append(snapshot, handlers[name])
	}
	mu.RUnlock()
	for _, handler := range snapshot {
		if code, ok := handler(); ok {
			return code, true
		}
	}
	return 0, false
}
