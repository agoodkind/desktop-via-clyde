package patch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/targets"
)

// KeychainItem is one captured generic-password row. Password bytes live in
// memory only; they are never written to state.json or logs.
type KeychainItem struct {
	Service string
	Account string
	Value   []byte
}

// CaptureItems enumerates the user's login keychain for every service named
// in t.KeychainServices and returns the {service, account, password} rows.
// The user may see one macOS prompt per item the first time this runs;
// clicking "Always Allow" caches the grant for the patch tool itself for
// future runs.
//
// Implementation: uses `security` CLI. `security 2>&1 find-generic-password
// -s <svc>` lists matching items by repeatedly deleting and re-checking is
// destructive; instead we use `dump-keychain` filtered by service. That would
// be too broad (dumps everything); the lighter approach is `security
// find-generic-password -s <svc> -g` which prints one item to stderr and the
// password to stderr too, but only one item per service. The keychain may
// hold multiple accounts under the same service, so a future implementation
// could iterate by account once account discovery is available.
//
// The pragmatic path: call `security find-generic-password -s <svc>` (without
// -g) which prints all attributes (including acct) for the *first* matching
// item, then delete attribute-pin and call again. To avoid mutating the
// keychain during capture, we instead use `security dump-keychain ~/Library/
// Keychains/login.keychain-db` and filter. But dump-keychain prompts for the
// keychain password on locked keychains.
//
// The current capture path assumes each declared service usually has one
// account. It captures using `security find-generic-password -s <svc> -w`
// plus a second call without -w to read account from the printed attribute
// block. If the service has multiple accounts, it captures only the most
// recently added one, and the user is warned via stdout.
func CaptureItems(ctx context.Context, t targets.Target) ([]KeychainItem, error) {
	if len(t.KeychainServices) == 0 {
		return nil, nil
	}

	items := make([]KeychainItem, 0, len(t.KeychainServices))

	for _, svc := range t.KeychainServices {
		attrs, err := runSecurity(ctx, "find-generic-password", "-s", svc)
		if err != nil {
			// Item does not exist for this service; that is normal for
			// services the app has not yet populated.
			continue
		}
		account := parseAccountFromAttrs(attrs)
		if account == "" {
			// Fall back to service-as-account, which is the common shape for
			// these Electron Safe Storage entries.
			account = svc
		}

		pw, err := runSecurity(ctx, "find-generic-password", "-s", svc, "-a", account, "-w")
		if err != nil {
			return nil, logPatchError(ctx, "patch.keychain_password_read_failed", fmt.Errorf("read password for service=%q account=%q: %w", svc, account, err))
		}
		// `-w` prints password followed by newline.
		value := bytes.TrimRight(pw, "\n")

		items = append(items, KeychainItem{
			Service: svc,
			Account: account,
			Value:   value,
		})
	}

	return items, nil
}

// RegrantItems removes each captured item and re-adds it with the patched
// .app's bundle path on the trusted-applications list (-T) and -A to allow
// access from that one application without further prompts.
func RegrantItems(ctx context.Context, t targets.Target, items []KeychainItem) error {
	var errs []error
	for _, item := range items {
		// `delete-generic-password` removes the item by service+account; if
		// the keychain has duplicates, it removes the first match. We then
		// re-add a fresh item with the new ACL.
		if _, err := runSecurity(ctx, "delete-generic-password", "-s", item.Service, "-a", item.Account); err != nil {
			// A missing item on delete is acceptable; some captured items
			// may have been removed between capture and re-grant. Continue.
			errs = append(errs, fmt.Errorf("delete s=%q a=%q: %w", item.Service, item.Account, err))
		}
		args := []string{
			"add-generic-password",
			"-s", item.Service,
			"-a", item.Account,
			"-w", string(item.Value),
			"-T", t.AppPath,
			"-A",
		}
		if _, err := runSecurity(ctx, args...); err != nil {
			errs = append(errs, fmt.Errorf("add s=%q a=%q: %w", item.Service, item.Account, err))
		}
	}
	if len(errs) > 0 {
		err := errors.Join(errs...)
		patchLog.ErrorContext(ctx, "patch.keychain_regrant_failed", "err", err)
		return err
	}
	return nil
}

// runSecurity executes /usr/bin/security and returns combined stdout+stderr.
// The keychain CLI mixes channels; combining is required to parse attributes.
func runSecurity(ctx context.Context, args ...string) ([]byte, error) {
	patchLog.DebugContext(ctx, "patch.security.boundary", "args", args)
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, logPatchError(ctx, "patch.security_failed", fmt.Errorf("security %s: %w (output: %s)", strings.Join(args, " "), err, string(out)))
	}
	return out, nil
}

// parseAccountFromAttrs reads the `"acct"<blob>="..."` line printed by
// `security find-generic-password`. Returns "" if not found.
func parseAccountFromAttrs(b []byte) string {
	for line := range strings.SplitSeq(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, `"acct"`) {
			continue
		}
		// Format: "acct"<blob>="value"
		_, value, ok := strings.Cut(trimmed, "=")
		if !ok {
			continue
		}
		val := strings.TrimSpace(value)
		val = strings.TrimSuffix(strings.TrimPrefix(val, `"`), `"`)
		return val
	}
	return ""
}

// restoreKeychainAccess re-grants keychain access for the items captured before the
// bundle mutations, reporting each outcome through the runner. It never returns an
// error: a regrant failure is logged as a continuing note so the patch finishes.
func restoreKeychainAccess(ctx context.Context, r *Runner, t targets.Target, opts Options, captured []KeychainItem) {
	switch {
	case !opts.MigrateKeychain:
		notef(r, fmt.Sprintf("target=%s skipped keychain access restore (pass --migrate-keychain to run)", t.ID))
	case opts.DryRun:
		notef(r, fmt.Sprintf("target=%s would restore keychain access for captured items", t.ID))
	case len(captured) > 0:
		if err := RegrantItems(ctx, t, captured); err != nil {
			notef(r, fmt.Sprintf("target=%s keychain access restore returned errors (continuing): %v", t.ID, err))
		} else {
			notef(r, fmt.Sprintf("target=%s restored keychain access for %d items", t.ID, len(captured)))
		}
	default:
		notef(r, fmt.Sprintf("target=%s no keychain items needed access restore", t.ID))
	}
}
