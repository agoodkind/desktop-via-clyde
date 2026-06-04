// Package hardreset resets per-bundle macOS privacy grants for one target.
package hardreset

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sort"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/bundleidentity"
	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

var hardResetLog = slog.With("component", "desktop-via-clyde", "subcomponent", "hardreset")

const (
	// AppHardResetCapability is the operation capability for privacy hard resets.
	AppHardResetCapability = "app.hard-reset"
)

var defaultServices = []string{
	"Accessibility",
	"AppleEvents",
	"Camera",
	"ListenEvent",
	"Microphone",
	"PostEvent",
	"ScreenCapture",
	"SystemPolicyAllFiles",
}

// RegisterOperations links hard-reset operation capabilities.
func RegisterOperations() error {
	if !catalog.HasOperationCapability(AppHardResetCapability) {
		if err := catalog.RegisterOperationCapability(AppHardResetCapability); err != nil {
			return logHardResetRegistrationError("register hard-reset capability", err)
		}
	}
	if err := operations.Register(AppHardResetCapability, Operation); err != nil {
		return logHardResetRegistrationError("register hard-reset operation", err)
	}
	return nil
}

// Operation runs the hard-reset operation for one configured target.
func Operation(ctx context.Context, req operations.Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	out := req.Out
	if out == nil {
		out = os.Stdout
	}
	if err := Run(ctx, *req.App, Options{
		DryRun: req.Flags.Bool("dry-run"),
		Out:    out,
		LogOut: req.LogOut,
	}); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.operation_failed", "err", err)
		return fmt.Errorf("hard-reset operation: %w",
			operations.Error(ctx, "operations.hard_reset_failed", "hard-reset app", err))
	}
	return nil
}

// Options controls one hard-reset invocation.
type Options struct {
	DryRun bool
	Out    io.Writer
	LogOut io.Writer
}

// Plan records the TCC reset commands for one target.
type Plan struct {
	TargetID  string
	AppPath   string
	Services  []string
	BundleIDs []string
}

// BuildPlan builds the hard-reset plan without mutating system state.
func BuildPlan(ctx context.Context, target targets.Target) (Plan, error) {
	bundleIDs, err := identitySet(ctx, target)
	if err != nil {
		return Plan{}, err
	}
	services := target.HardResetServices
	if len(services) == 0 {
		services = defaultServices
	}
	return Plan{
		TargetID:  target.ID,
		AppPath:   target.AppPath,
		Services:  sortedUnique(services),
		BundleIDs: bundleIDs,
	}, nil
}

// Run executes or prints the hard-reset plan.
func Run(ctx context.Context, target targets.Target, opts Options) error {
	hardResetLog.InfoContext(ctx, "hardreset.run.boundary", "target", target.ID, "app_path", target.AppPath, "dry_run", opts.DryRun)
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	opts.Out = out
	plan, err := BuildPlan(ctx, target)
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.build_plan_failed", "target", target.ID, "err", err)
		return err
	}
	if len(plan.BundleIDs) == 0 {
		return fmt.Errorf("target %s has no bundle IDs to reset", target.ID)
	}
	if _, err := fmt.Fprintf(out, "target=%s hard-reset app=%s\n", plan.TargetID, plan.AppPath); err != nil {
		return fmt.Errorf("write hard-reset header: %w", err)
	}
	if _, err := fmt.Fprintf(out, "services=%s\n", strings.Join(plan.Services, ",")); err != nil {
		return fmt.Errorf("write hard-reset services: %w", err)
	}
	if _, err := fmt.Fprintf(out, "bundle_ids=%s\n", strings.Join(plan.BundleIDs, ",")); err != nil {
		return fmt.Errorf("write hard-reset bundle IDs: %w", err)
	}
	for _, service := range plan.Services {
		for _, bundleID := range plan.BundleIDs {
			if err := resetService(ctx, opts, service, bundleID); err != nil {
				hardResetLog.ErrorContext(ctx, "hardreset.reset_service_failed", "target", target.ID, "service", service, "bundle_id", bundleID, "err", err)
				return err
			}
		}
	}
	if opts.DryRun {
		_, err := fmt.Fprintln(out, "aftercare=report-only")
		if err != nil {
			return fmt.Errorf("write hard-reset aftercare: %w", err)
		}
		return nil
	}
	_, err = fmt.Fprintln(out, "aftercare=report-only")
	if err != nil {
		return fmt.Errorf("write hard-reset aftercare: %w", err)
	}
	return nil
}

func identitySet(ctx context.Context, target targets.Target) ([]string, error) {
	values := make([]string, 0, 1+len(target.BundleIDAliases)+len(target.HelperBundleIDs))
	values = append(values, target.BundleID)
	values = append(values, target.BundleIDAliases...)
	values = append(values, target.HelperBundleIDs...)

	entries, err := bundleidentity.Scan(ctx, target.AppPath, bundleidentity.ScanOptions{
		IncludeSignatures: false,
		SignatureReader:   nil,
	})
	if err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.identity_set_scan_failed", "target", target.ID, "app_path", target.AppPath, "err", err)
		return nil, fmt.Errorf("scan runtime bundle identities: %w", err)
	}
	for _, entry := range entries {
		if entry.RuntimeCode && entry.BundleID != "" {
			values = append(values, entry.BundleID)
		}
	}
	return sortedUnique(values), nil
}

func resetService(ctx context.Context, opts Options, service string, bundleID string) error {
	hardResetLog.InfoContext(ctx, "hardreset.reset_service.boundary", "service", service, "bundle_id", bundleID, "dry_run", opts.DryRun)
	args := []string{"reset", service, bundleID}
	if opts.DryRun {
		_, err := fmt.Fprintf(opts.Out, "dry-run: /usr/bin/tccutil %s\n", strings.Join(args, " "))
		if err != nil {
			return fmt.Errorf("write dry-run tccutil command: %w", err)
		}
		return nil
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/tccutil", args...)
	if opts.LogOut != nil {
		cmd.Stdout = opts.LogOut
		cmd.Stderr = opts.LogOut
	} else {
		cmd.Stdout = opts.Out
		cmd.Stderr = opts.Out
	}
	if err := cmd.Run(); err != nil {
		hardResetLog.ErrorContext(ctx, "hardreset.tccutil_failed", "service", service, "bundle_id", bundleID, "err", err)
		return fmt.Errorf("run tccutil %s: %w", strings.Join(args, " "), err)
	}
	_, err := fmt.Fprintf(opts.Out, "reset: /usr/bin/tccutil %s\n", strings.Join(args, " "))
	if err != nil {
		return fmt.Errorf("write tccutil reset command: %w", err)
	}
	return nil
}

func logHardResetRegistrationError(message string, err error) error {
	hardResetLog.Error("hardreset.registration_failed", "message", message, "err", err)
	return fmt.Errorf("%s: %w", message, err)
}

func sortedUnique(values []string) []string {
	seen := map[string]bool{}
	results := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		results = append(results, trimmed)
	}
	sort.Strings(results)
	return results
}
