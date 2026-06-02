// Package operations dispatches configured command capabilities onto the
// concrete app and CLI behavior linked into this binary.
package operations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/clioutput"
	"goodkind.io/desktop-via-clyde/internal/response"
	"goodkind.io/desktop-via-clyde/internal/statusreport"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// Handler runs one configured operation capability.
type Handler func(context.Context, Request) error

var (
	handlersMu sync.RWMutex
	handlers   = map[string]Handler{}
)

var operationsLog = slog.With("component", "desktop-via-clyde", "subcomponent", "operations")

// AppStatusCapability is the operation capability for app status output.
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
	LogOut     io.Writer
	App        *targets.Target
	CLI        *targets.CLIProgram
	Capability string
	Flags      FlagValues
	Format     clioutput.Format
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

// RegisterCoreHandlers links operation handlers owned by this package.
func RegisterCoreHandlers() error {
	if !catalog.HasOperationCapability(AppStatusCapability) {
		if err := catalog.RegisterOperationCapability(AppStatusCapability); err != nil {
			return logOperationRegistrationError("register app status capability", err)
		}
	}
	if err := Register(AppStatusCapability, AppStatus); err != nil {
		return logOperationRegistrationError("register app status operation", err)
	}
	return nil
}

// AppStatus prints status for one configured desktop app.
func AppStatus(ctx context.Context, req Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	report, err := statusreport.BuildTarget(ctx, *req.App)
	if err != nil {
		return Error(ctx, "operations.app_status_failed", "print app status", err)
	}
	if req.Format == clioutput.FormatJSON {
		body, err := json.Marshal(report)
		if err != nil {
			return Error(ctx, "operations.app_status_marshal_failed", "marshal app status", err)
		}
		if err := response.WriteJSON(ctx, req.Out, body, response.JSONIndented); err != nil {
			return Error(ctx, "operations.app_status_write_json_failed", "write app status", err)
		}
		return nil
	}
	if err := statusreport.WriteText(req.Out, report); err != nil {
		return Error(ctx, "operations.app_status_write_text_failed", "write app status", err)
	}
	return nil
}

// Error logs and wraps one operation failure.
func Error(ctx context.Context, event string, message string, err error) error {
	slog.Default().ErrorContext(ctx, event, "err", err)
	return errors.New(message + ": " + err.Error())
}

func logOperationRegistrationError(message string, err error) error {
	operationsLog.Error("operations.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}
