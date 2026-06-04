// Package bundleidentity discovers executable bundle identities inside macOS
// application bundles.
package bundleidentity

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"howett.net/plist"
)

var bundleIdentityLog = slog.With("component", "desktop-via-clyde", "subcomponent", "bundleidentity")

type bundleExtension string

const (
	bundleExtensionApp       bundleExtension = ".app"
	bundleExtensionFramework bundleExtension = ".framework"
	bundleExtensionXPC       bundleExtension = ".xpc"
	bundleExtensionBundle    bundleExtension = ".bundle"
)

type signatureField string

const (
	signatureFieldIdentifier     signatureField = "Identifier"
	signatureFieldTeamIdentifier signatureField = "TeamIdentifier"
)

// SignatureReader reads codesigning metadata for one code object.
type SignatureReader func(context.Context, string) (Signature, error)

// ScanOptions controls bundle identity discovery.
type ScanOptions struct {
	IncludeSignatures bool
	SignatureReader   SignatureReader
}

// Entry records one discovered bundle identity.
type Entry struct {
	RootPath       string `json:"root_path"`
	RelativePath   string `json:"relative_path"`
	InfoPlistPath  string `json:"info_plist_path"`
	BundleID       string `json:"bundle_id"`
	PackageType    string `json:"package_type"`
	Executable     string `json:"executable"`
	RuntimeCode    bool   `json:"runtime_code"`
	Identifier     string `json:"identifier,omitempty"`
	TeamID         string `json:"team_id,omitempty"`
	SignatureError string `json:"signature_error,omitempty"`
}

// Signature contains the codesigning identity fields used by this tool.
type Signature struct {
	Identifier string
	TeamID     string
}

type infoPlist struct {
	CFBundleIdentifier  string `plist:"CFBundleIdentifier"`
	CFBundlePackageType string `plist:"CFBundlePackageType"`
	CFBundleExecutable  string `plist:"CFBundleExecutable"`
}

// Scan walks root and returns every bundle identity declared by an Info.plist.
func Scan(ctx context.Context, root string, opts ScanOptions) ([]Entry, error) {
	root = filepath.Clean(root)
	bundleIdentityLog.DebugContext(ctx, "bundleidentity.scan.boundary", "root", root, "include_signatures", opts.IncludeSignatures)
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		bundleIdentityLog.ErrorContext(ctx, "bundleidentity.scan.stat_failed", "root", root, "err", err)
		return nil, fmt.Errorf("stat bundle identity root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("bundle identity root %s is not a directory", root)
	}

	reader := opts.SignatureReader
	if reader == nil {
		reader = ReadSignature
	}

	seenRoots := map[string]bool{}
	entries := make([]Entry, 0)
	walkErr := filepath.WalkDir(root, func(path string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if dirEntry.IsDir() || filepath.Base(path) != "Info.plist" {
			return nil
		}
		bundleRoot, ok := bundleRootForInfoPlist(path)
		if !ok {
			return nil
		}
		bundleRoot = filepath.Clean(bundleRoot)
		if seenRoots[bundleRoot] {
			return nil
		}
		seenRoots[bundleRoot] = true

		entry, err := readEntry(root, bundleRoot, path)
		if err != nil {
			return err
		}
		if opts.IncludeSignatures {
			signature, sigErr := reader(ctx, bundleRoot)
			if sigErr != nil {
				entry.SignatureError = sigErr.Error()
			} else {
				entry.Identifier = signature.Identifier
				entry.TeamID = signature.TeamID
			}
		}
		entries = append(entries, entry)
		return nil
	})
	if walkErr != nil {
		bundleIdentityLog.ErrorContext(ctx, "bundleidentity.scan.walk_failed", "root", root, "err", walkErr)
		return nil, fmt.Errorf("walk bundle identities under %s: %w", root, walkErr)
	}

	sort.Slice(entries, func(i int, j int) bool {
		if entries[i].RelativePath == entries[j].RelativePath {
			return entries[i].BundleID < entries[j].BundleID
		}
		return entries[i].RelativePath < entries[j].RelativePath
	})
	return entries, nil
}

// ReadSignature reads codesigning metadata from /usr/bin/codesign.
func ReadSignature(ctx context.Context, codePath string) (Signature, error) {
	bundleIdentityLog.DebugContext(ctx, "bundleidentity.read_signature.boundary", "path", codePath)
	cmd := exec.CommandContext(ctx, "/usr/bin/codesign", "-dv", codePath)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			bundleIdentityLog.ErrorContext(ctx, "bundleidentity.read_signature.codesign_failed", "path", codePath, "err", err)
			return Signature{Identifier: "", TeamID: ""}, fmt.Errorf("codesign -dv %s: %w", codePath, err)
		}
		bundleIdentityLog.ErrorContext(ctx, "bundleidentity.read_signature.codesign_failed", "path", codePath, "err", err, "output", text)
		return Signature{Identifier: "", TeamID: ""}, fmt.Errorf("codesign -dv %s: %w: %s", codePath, err, text)
	}
	return parseSignature(text), nil
}

// RuntimeNestedEntries returns runtime code entries below root but excludes root itself.
func RuntimeNestedEntries(entries []Entry, root string, preserved []string) []Entry {
	root = filepath.Clean(root)
	results := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		if !entry.RuntimeCode {
			continue
		}
		if samePath(entry.RootPath, root) {
			continue
		}
		if isPreserved(entry.RelativePath, preserved) {
			continue
		}
		results = append(results, entry)
	}
	sort.Slice(results, func(i int, j int) bool {
		leftDepth := pathDepth(results[i].RelativePath)
		rightDepth := pathDepth(results[j].RelativePath)
		if leftDepth == rightDepth {
			return results[i].RelativePath < results[j].RelativePath
		}
		return leftDepth > rightDepth
	})
	return results
}

// IsPreserved reports whether a relative path is covered by a preserved code path.
func IsPreserved(relPath string, preserved []string) bool {
	return isPreserved(filepath.ToSlash(filepath.Clean(relPath)), preserved)
}

func readEntry(scanRoot string, bundleRoot string, infoPath string) (Entry, error) {
	var parsed infoPlist
	data, err := os.ReadFile(infoPath)
	if err != nil {
		bundleIdentityLog.Error("bundleidentity.read_entry.info_plist_read_failed", "path", infoPath, "err", err)
		return Entry{}, fmt.Errorf("read Info.plist %s: %w", infoPath, err)
	}
	if _, err := plist.Unmarshal(data, &parsed); err != nil {
		bundleIdentityLog.Error("bundleidentity.read_entry.info_plist_parse_failed", "path", infoPath, "err", err)
		return Entry{}, fmt.Errorf("parse Info.plist %s: %w", infoPath, err)
	}
	relPath, err := filepath.Rel(scanRoot, bundleRoot)
	if err != nil {
		bundleIdentityLog.Error("bundleidentity.read_entry.relative_path_failed", "root", scanRoot, "bundle_root", bundleRoot, "err", err)
		return Entry{}, fmt.Errorf("relativize bundle root %s under %s: %w", bundleRoot, scanRoot, err)
	}
	if relPath == "." {
		relPath = "."
	} else {
		relPath = filepath.ToSlash(relPath)
	}
	return Entry{
		RootPath:       bundleRoot,
		RelativePath:   relPath,
		InfoPlistPath:  infoPath,
		BundleID:       strings.TrimSpace(parsed.CFBundleIdentifier),
		PackageType:    strings.TrimSpace(parsed.CFBundlePackageType),
		Executable:     strings.TrimSpace(parsed.CFBundleExecutable),
		RuntimeCode:    hasRuntimeExecutable(bundleRoot, infoPath, parsed.CFBundleExecutable),
		Identifier:     "",
		TeamID:         "",
		SignatureError: "",
	}, nil
}

func bundleRootForInfoPlist(infoPath string) (string, bool) {
	current := filepath.Dir(infoPath)
	for {
		switch bundleExtension(filepath.Ext(current)) {
		case bundleExtensionApp, bundleExtensionFramework, bundleExtensionXPC, bundleExtensionBundle:
			return current, true
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false
		}
		current = parent
	}
}

func hasRuntimeExecutable(bundleRoot string, infoPath string, executable string) bool {
	executable = strings.TrimSpace(executable)
	if executable == "" {
		return false
	}
	candidates := []string{
		filepath.Join(bundleRoot, "Contents", "MacOS", executable),
		filepath.Join(bundleRoot, executable),
		filepath.Join(bundleRoot, "Versions", "Current", executable),
	}
	versionedCandidates, err := filepath.Glob(filepath.Join(bundleRoot, "Versions", "*", executable))
	if err == nil {
		candidates = append(candidates, versionedCandidates...)
	}
	infoDir := filepath.Dir(infoPath)
	if filepath.Base(infoDir) == "Resources" {
		candidates = append(candidates, filepath.Join(filepath.Dir(infoDir), executable))
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func parseSignature(text string) Signature {
	signature := Signature{Identifier: "", TeamID: ""}
	for line := range strings.SplitSeq(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch signatureField(key) {
		case signatureFieldIdentifier:
			signature.Identifier = strings.TrimSpace(value)
		case signatureFieldTeamIdentifier:
			signature.TeamID = strings.TrimSpace(value)
		}
	}
	return signature
}

func samePath(left string, right string) bool {
	return filepath.Clean(left) == filepath.Clean(right)
}

func isPreserved(relPath string, preserved []string) bool {
	relPath = filepath.ToSlash(filepath.Clean(relPath))
	for _, preservedPath := range preserved {
		cleanPreserved := filepath.ToSlash(filepath.Clean(preservedPath))
		if relPath == cleanPreserved || strings.HasPrefix(relPath, cleanPreserved+"/") {
			return true
		}
	}
	return false
}

func pathDepth(path string) int {
	if path == "." || path == "" {
		return 0
	}
	return strings.Count(filepath.ToSlash(path), "/")
}
