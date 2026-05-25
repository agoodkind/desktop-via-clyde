// Package signing centralizes local Developer ID signing helpers.
package signing

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/paths"
)

var (
	identityLineRE = regexp.MustCompile(`^\s*\d+\)\s+([0-9A-F]{40})\s+"([^"]+)"\s*$`)
	signingLog     = slog.With("component", "desktop-via-clyde", "subcomponent", "signing")
)

// ResolveIdentity returns the SHA-1 hash of the first codesigning identity
// whose common name matches paths.SignIdentity. The keychain may hold multiple
// certs with the same CN, and codesign rejects an ambiguous CN, so callers sign
// with the resolved hash.
func ResolveIdentity(ctx context.Context, dryRun bool) (string, error) {
	signingLog.DebugContext(ctx, "signing.resolve_identity", "dry_run", dryRun)
	if dryRun {
		return paths.SignIdentity, nil
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/security", "find-identity", "-v", "-p", "codesigning")
	out, err := cmd.Output()
	if err != nil {
		signingLog.ErrorContext(ctx, "signing.resolve_identity.find_failed", "err", err)
		return "", fmt.Errorf("security find-identity: %w", err)
	}
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		m := identityLineRE.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		if m[2] == paths.SignIdentity {
			return m[1], nil
		}
	}
	if err := scanner.Err(); err != nil {
		signingLog.ErrorContext(ctx, "signing.resolve_identity.scan_failed", "err", err)
		return "", fmt.Errorf("scan security find-identity output: %w", err)
	}
	signingLog.ErrorContext(ctx, "signing.resolve_identity.not_found", "identity", paths.SignIdentity, "err", errors.New("codesigning identity not found"))
	return "", fmt.Errorf("no codesigning identity matches %q", paths.SignIdentity)
}

// RuntimeEntitlementsArgs returns the standard hardened-runtime codesign
// arguments with an entitlement plist.
func RuntimeEntitlementsArgs(id string, entFile string, codePath string) []string {
	return []string{
		"--force",
		"--sign",
		id,
		"--options",
		"runtime",
		"--entitlements",
		entFile,
		codePath,
	}
}

// RuntimeTimestampEntitlementsArgs returns the upstream Codex release signing
// argument shape for standalone CLI binaries.
func RuntimeTimestampEntitlementsArgs(id string, entFile string, codePath string) []string {
	return []string{
		"--force",
		"--options",
		"runtime",
		"--timestamp",
		"--entitlements",
		entFile,
		"--sign",
		id,
		codePath,
	}
}

// RuntimeArgs returns the standard hardened-runtime codesign arguments.
func RuntimeArgs(id string, codePath string) []string {
	return []string{
		"--force",
		"--sign",
		id,
		"--options",
		"runtime",
		codePath,
	}
}
