// Package operations dispatches configured command capabilities onto the
// concrete app and CLI behavior linked into this binary.
package operations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/state"
	"goodkind.io/desktop-via-clyde/internal/targets"
	"howett.net/plist"
)

// Handler runs one configured operation capability.
type Handler func(context.Context, Request) error

var (
	handlersMu sync.RWMutex
	handlers   = map[string]Handler{}
)

const AppStatusCapability = "app.status"

// FlagValues holds parsed command flag values keyed by flag name.
type FlagValues struct {
	strings map[string]string
	bools   map[string]bool
}

// NewFlagValues builds an empty FlagValues container.
func NewFlagValues() FlagValues {
	return FlagValues{
		strings: map[string]string{},
		bools:   map[string]bool{},
	}
}

// SetString stores one string flag value.
func (f FlagValues) SetString(name string, value string) {
	f.strings[name] = value
}

// SetBool stores one bool flag value.
func (f FlagValues) SetBool(name string, value bool) {
	f.bools[name] = value
}

// String returns one stored string flag value.
func (f FlagValues) String(name string) string {
	return f.strings[name]
}

// Bool returns one stored bool flag value.
func (f FlagValues) Bool(name string) bool {
	return f.bools[name]
}

// Request describes one command dispatch into a typed capability.
type Request struct {
	Out        io.Writer
	App        *targets.Target
	CLI        *targets.CLIProgram
	Capability string
	Flags      FlagValues
}

// Run dispatches one operation capability with parsed flags and an optional
// app or CLI declaration.
func Run(ctx context.Context, req Request) error {
	handler, ok := Lookup(req.Capability)
	if !ok {
		return fmt.Errorf("unknown operation capability %q", req.Capability)
	}
	return handler(ctx, req)
}

// Register links an operation capability to its runtime handler.
func Register(capability string, handler Handler) error {
	if !catalog.HasOperationCapability(capability) {
		return fmt.Errorf("operation capability %q is not linked", capability)
	}
	if handler == nil {
		return fmt.Errorf("operation capability %q handler is required", capability)
	}
	handlersMu.Lock()
	defer handlersMu.Unlock()
	if _, ok := handlers[capability]; ok {
		return fmt.Errorf("operation capability %q handler is already registered", capability)
	}
	handlers[capability] = handler
	return nil
}

// Lookup returns the registered handler for one operation capability.
func Lookup(capability string) (Handler, bool) {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	handler, ok := handlers[capability]
	return handler, ok
}

// RegisteredCapabilities returns operation capabilities with runtime handlers.
func RegisteredCapabilities() []string {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	names := make([]string, 0, len(handlers))
	for name := range handlers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// RegisterCoreHandlers links operation handlers owned by this package.
func RegisterCoreHandlers() error {
	if !catalog.HasOperationCapability(AppStatusCapability) {
		if err := catalog.RegisterOperationCapability(AppStatusCapability); err != nil {
			return err
		}
	}
	return Register(AppStatusCapability, AppStatus)
}

func ensureOperationCapabilityRegistered(capability string) error {
	if catalog.HasOperationCapability(capability) {
		return nil
	}
	if err := catalog.RegisterOperationCapability(capability); err != nil {
		return err
	}
	return nil
}

// AppStatus prints status for one configured desktop app.
func AppStatus(ctx context.Context, req Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	if err := writeAppStatus(ctx, req.Out, *req.App); err != nil {
		return Error(ctx, "operations.app_status_failed", "print app status", err)
	}
	return nil
}

func writeAppStatus(ctx context.Context, out io.Writer, target targets.Target) error {
	multiState, err := state.Load(paths.StateFile())
	if err != nil {
		return Error(ctx, "operations.app_status_load_state_failed", "load state file "+paths.StateFile(), err)
	}
	fmt.Fprintf(out, "state file: %s\n", paths.StateFile())
	fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", "TARGET", "STATE", "VERSION", "NOTES")

	entry, hasState := multiState.Targets[target.ID]
	appExists, appStatErr := pathExists(ctx, target.AppPath)
	if appStatErr != nil {
		return Error(ctx, "operations.app_status_stat_app_failed", "stat app bundle "+target.AppPath, appStatErr)
	}
	if !appExists {
		fmt.Fprintf(out, "%-8s  %-9s  %-20s  bundle missing at %s\n", target.ID, "absent", "-", target.AppPath)
		return nil
	}
	if !hasState {
		fmt.Fprintf(out, "%-8s  %-9s  %-20s  bundle present, no state entry\n", target.ID, "clean", "-")
		return nil
	}

	currentVersion := readBundleVersion(target)
	stateLabel := "patched"
	notes := fmt.Sprintf("signed-as=%q", entry.SignIdentity)
	realPathExists, realPathErr := pathExists(ctx, paths.RealBinaryPath(target))
	if realPathErr != nil {
		return Error(ctx, "operations.app_status_stat_real_failed", "stat restored binary path "+paths.RealBinaryPath(target), realPathErr)
	}
	if !realPathExists {
		stateLabel = "drifted"
		notes = notes + "; " + target.ExecName + ".real missing"
	} else if currentVersion != "" && currentVersion != entry.PatchedVersion {
		stateLabel = "drifted"
		notes += fmt.Sprintf("; current version %s != patched %s", currentVersion, entry.PatchedVersion)
	}
	fmt.Fprintf(out, "%-8s  %-9s  %-20s  %s\n", target.ID, stateLabel, entry.PatchedVersion, notes)
	return nil
}

// Error logs and wraps one operation failure.
func Error(ctx context.Context, event string, message string, err error) error {
	slog.Default().ErrorContext(ctx, event, "err", err)
	return errors.New(message + ": " + err.Error())
}

func pathExists(ctx context.Context, path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	slog.Default().ErrorContext(ctx, "operations.path_exists_stat_failed", "path", path, "err", err)
	return false, errors.New("stat " + path + ": " + err.Error())
}

func readBundleVersion(target targets.Target) string {
	info, err := readInfoPlist(paths.InfoPlistPath(target))
	if err != nil {
		return ""
	}
	return info.CFBundleVersion
}

type infoPlist struct {
	CFBundleVersion string `plist:"CFBundleVersion"`
}

func readInfoPlist(path string) (infoPlist, error) {
	var info infoPlist
	data, err := os.ReadFile(path)
	if err != nil {
		return info, err
	}
	_, err = plist.Unmarshal(data, &info)
	return info, err
}
