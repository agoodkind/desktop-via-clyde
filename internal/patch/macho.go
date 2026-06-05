package patch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"goodkind.io/desktop-via-clyde/internal/bundleidentity"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// discoverMachOCodeSignPaths walks the app bundle and returns every nested
// Mach-O object that should be re-signed, excluding the main and real binaries,
// preserved subtrees, and .dSYM debug bundles.
func discoverMachOCodeSignPaths(ctx context.Context, t targets.Target) ([]string, error) {
	root := filepath.Clean(t.AppPath)
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		patchLog.ErrorContext(ctx, "patch.macho_code_root_stat_failed", "root", root, "err", err)
		return nil, fmt.Errorf("stat Mach-O code root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("Mach-O code root %s is not a directory", root)
	}
	excludedPaths := []string{
		filepath.Clean(paths.MainBinaryPath(t)),
		filepath.Clean(paths.RealBinaryPath(t)),
	}
	results := make([]string, 0)
	walkErr := filepath.WalkDir(root, func(path string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if dirEntry.IsDir() {
			return skipMachODir(ctx, root, path, dirEntry, t.PreservedNestedCodePaths)
		}
		signPath, err := machOCodeSignPath(ctx, root, path, dirEntry, excludedPaths, t.PreservedNestedCodePaths)
		if err != nil {
			return err
		}
		if signPath != "" {
			results = append(results, signPath)
		}
		return nil
	})
	if walkErr != nil {
		patchLog.ErrorContext(ctx, "patch.macho_code_walk_failed", "root", root, "err", walkErr)
		return nil, fmt.Errorf("walk Mach-O code objects under %s: %w", root, walkErr)
	}
	sort.Strings(results)
	return results, nil
}

// skipMachODir decides how WalkDir treats a directory during Mach-O discovery.
// It returns [filepath.SkipDir] for .dSYM debug bundles and preserved subtrees.
func skipMachODir(ctx context.Context, root, path string, dirEntry fs.DirEntry, preserved []string) error {
	if path == root {
		return nil
	}
	// Skip .dSYM debug bundles. Their DWARF payloads carry Mach-O magic but are
	// debug companions, not code objects that should be signed.
	if filepath.Ext(dirEntry.Name()) == ".dSYM" {
		return filepath.SkipDir
	}
	relPath, err := filepath.Rel(root, path)
	if err != nil {
		patchLog.ErrorContext(ctx, "patch.macho_code_dir_rel_failed", "root", root, "path", path, "err", err)
		return fmt.Errorf("relativize %s under %s: %w", path, root, err)
	}
	if bundleidentity.IsPreserved(relPath, preserved) {
		return filepath.SkipDir
	}
	return nil
}

// machOCodeSignPath returns the cleaned path of a regular file when it is a
// Mach-O object that should be signed, or "" when the file should be skipped.
func machOCodeSignPath(ctx context.Context, root, path string, dirEntry fs.DirEntry, excluded, preserved []string) (string, error) {
	if !dirEntry.Type().IsRegular() {
		return "", nil
	}
	cleanPath := filepath.Clean(path)
	if containsCleanPath(excluded, cleanPath) {
		return "", nil
	}
	relPath, err := filepath.Rel(root, cleanPath)
	if err != nil {
		patchLog.ErrorContext(ctx, "patch.macho_code_file_rel_failed", "root", root, "path", cleanPath, "err", err)
		return "", fmt.Errorf("relativize %s under %s: %w", cleanPath, root, err)
	}
	if bundleidentity.IsPreserved(relPath, preserved) {
		return "", nil
	}
	machO, err := isMachOFile(ctx, cleanPath)
	if err != nil {
		return "", err
	}
	if !machO {
		return "", nil
	}
	return cleanPath, nil
}

func isMachOFile(ctx context.Context, path string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		patchLog.ErrorContext(ctx, "patch.macho_code_open_failed", "path", path, "err", err)
		return false, fmt.Errorf("open possible Mach-O file %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()

	magic := make([]byte, 4)
	if _, err := io.ReadFull(file, magic); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		patchLog.ErrorContext(ctx, "patch.macho_code_read_failed", "path", path, "err", err)
		return false, fmt.Errorf("read possible Mach-O file %s: %w", path, err)
	}
	switch machOMagic(string(magic)) {
	case machOMagic32BE, machOMagic32LE,
		machOMagic64BE, machOMagic64LE,
		machOMagicFat32BE, machOMagicFat32LE,
		machOMagicFat64BE, machOMagicFat64LE:
		return true, nil
	default:
		return false, nil
	}
}

func containsCleanPath(values []string, needle string) bool {
	for _, value := range values {
		if filepath.Clean(value) == needle {
			return true
		}
	}
	return false
}
