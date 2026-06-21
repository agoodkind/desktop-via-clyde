package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/blake2b"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

var errTarUnsafePath = errors.New("unsafe tar path")

func fetchTauriMinisignManifest(ctx context.Context, t targets.Target, currentVersion string) (updateManifest, error) {
	target, arch := splitTauriPlatform(t.Updater.Platform)
	endpoint := renderUpdaterTemplate(t.Updater.URLTemplate, map[string]string{
		"target":   target,
		"arch":     arch,
		"version":  currentVersion,
		"platform": t.Updater.Platform,
	})
	body, err := fetchURL(ctx, endpoint, renderUserAgent(t.Updater.UserAgent, currentVersion), "application/json", 1<<16)
	if err != nil {
		if errors.Is(err, errNoUpdate) {
			return updateManifest{}, errNoUpdate
		}
		return updateManifest{}, err
	}
	return parseTauriMinisignManifest(body)
}

func parseTauriMinisignManifest(body []byte) (updateManifest, error) {
	m := tauriMinisignManifest{
		Version:   "",
		Notes:     "",
		PubDate:   "",
		URL:       "",
		Signature: "",
		Format:    "",
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return updateManifest{}, logUpgradeErrorNoContext("upgrade.tauri_minisign_manifest_parse_failed", fmt.Errorf("parse Tauri minisign manifest JSON: %w (body=%s)", err, string(body)))
	}
	version := strings.TrimSpace(m.Version)
	if version == "" {
		return updateManifest{}, errors.New("tauri minisign manifest is missing version")
	}
	format := strings.TrimSpace(m.Format)
	if format != "app" {
		return updateManifest{}, fmt.Errorf("tauri minisign manifest format = %q, want app", format)
	}
	return updateManifest{
		URL:       strings.TrimSpace(m.URL),
		Name:      version,
		Signature: strings.TrimSpace(m.Signature),
		Format:    format,
	}, nil
}

func tauriMinisignArchiveName(t targets.Target) string {
	baseName := strings.TrimSpace(t.ExecName)
	if baseName == "" {
		baseName = filepath.Base(t.AppPath)
	}
	baseName = filepath.Base(baseName)
	baseName = strings.TrimSuffix(baseName, ".app")
	if baseName == "" || baseName == "." || baseName == string(os.PathSeparator) {
		baseName = "update"
	}
	return baseName + ".app.tar.gz"
}

func verifyTauriMinisignArchive(ctx context.Context, r *patch.Runner, t targets.Target, m updateManifest, archivePath string, dryRun bool) error {
	notef(r, fmt.Sprintf("target=%s verify minisign signature for %s", t.ID, archivePath))
	if m.Signature == "" {
		return logUpgradeError(ctx, "upgrade.tauri_minisign_signature_missing", errors.New("tauri minisign manifest is missing signature"))
	}
	if dryRun {
		return nil
	}
	signatureFile, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.tauri_minisign_signature_decode_failed", fmt.Errorf("decode tauri minisign signature: %w", err))
	}
	signatureAlgorithm, err := minisignSignatureAlgorithm(ctx, signatureFile)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.tauri_minisign_signature_parse_failed", fmt.Errorf("parse minisign signature for %s: %w", archivePath, err))
	}
	switch signatureAlgorithm {
	case minisignPrehashAlgorithm:
		digest, err := blake2b512File(ctx, archivePath)
		if err != nil {
			return err
		}
		if err := verifyMinisignDigest(ctx, t.Updater.MinisignPublicKey, digest, signatureFile); err != nil {
			return logUpgradeError(ctx, "upgrade.tauri_minisign_verify_failed", fmt.Errorf("verify minisign signature for %s: %w", archivePath, err))
		}
	case minisignLegacyAlgorithm:
		artifact, err := os.ReadFile(archivePath)
		if err != nil {
			return logUpgradeError(ctx, "upgrade.tauri_minisign_archive_read_failed", fmt.Errorf("read downloaded archive for minisign verification: %w", err))
		}
		if err := verifyMinisign(ctx, t.Updater.MinisignPublicKey, artifact, signatureFile); err != nil {
			return logUpgradeError(ctx, "upgrade.tauri_minisign_verify_failed", fmt.Errorf("verify minisign signature for %s: %w", archivePath, err))
		}
	default:
		return logUpgradeError(ctx, "upgrade.tauri_minisign_verify_failed", fmt.Errorf("minisign signature algorithm %q is unsupported", signatureAlgorithm))
	}
	return nil
}

func blake2b512File(ctx context.Context, path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, logUpgradeError(ctx, "upgrade.minisign_hash", fmt.Errorf("open archive %s: %w", path, err))
	}
	defer func() { _ = file.Close() }()
	hasher, err := blake2b.New512(nil)
	if err != nil {
		return nil, logUpgradeError(ctx, "upgrade.minisign_hash", fmt.Errorf("create blake2b-512 hasher: %w", err))
	}
	if _, err := io.Copy(hasher, file); err != nil {
		return nil, logUpgradeError(ctx, "upgrade.minisign_hash", fmt.Errorf("read archive into blake2b-512 hasher: %w", err))
	}
	return hasher.Sum(nil), nil
}

func extractTarGz(ctx context.Context, r *patch.Runner, tarGzPath, staging string, t targets.Target, dryRun bool) (string, error) {
	extractDir := filepath.Join(staging, "extracted")
	expected := filepath.Join(extractDir, filepath.Base(t.AppPath))
	if dryRun {
		return expected, nil
	}
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", logUpgradeError(ctx, "upgrade.extract_dir_create_failed", fmt.Errorf("create extract dir: %w", err))
	}
	if err := writeTarGzArchive(ctx, tarGzPath, extractDir); err != nil {
		return "", logUpgradeError(ctx, "upgrade.tauri_extract_tar_gz_failed", fmt.Errorf("extract tar.gz: %w", err))
	}
	if _, err := os.Stat(expected); err == nil {
		return expected, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", logUpgradeError(ctx, "upgrade.extracted_app_stat_failed", fmt.Errorf("stat expected app %s: %w", expected, err))
	}
	fallbackApp, err := singleExtractedAppRoot(extractDir)
	if err != nil {
		return "", logUpgradeError(ctx, "upgrade.extracted_app_stat_failed", fmt.Errorf("expected %s inside tar.gz: %w", filepath.Base(t.AppPath), err))
	}
	notef(r, fmt.Sprintf("target=%s extracted app root %s differs from configured app name %s", t.ID, filepath.Base(fallbackApp), filepath.Base(t.AppPath)))
	return fallbackApp, nil
}

func writeTarGzArchive(ctx context.Context, tarGzPath string, extractDir string) error {
	file, err := os.Open(tarGzPath)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.tar_gz_open_failed", fmt.Errorf("open %s: %w", tarGzPath, err))
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.tar_gz_stream_open_failed", fmt.Errorf("open gzip stream: %w", err))
	}
	defer func() { _ = gzipReader.Close() }()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return logUpgradeError(ctx, "upgrade.tar_header_read_failed", fmt.Errorf("read tar header: %w", err))
		}
		if err := writeTarEntry(ctx, tarReader, header, extractDir); err != nil {
			return err
		}
	}
}

func writeTarEntry(ctx context.Context, tarReader *tar.Reader, header *tar.Header, extractDir string) error {
	upgradeLog.DebugContext(ctx, "upgrade.tar_entry", "name", header.Name, "type", int(header.Typeflag))
	targetPath, err := safeExtractPath(ctx, extractDir, header.Name)
	if err != nil {
		return err
	}
	switch header.Typeflag {
	case tar.TypeDir:
		if err := os.MkdirAll(targetPath, header.FileInfo().Mode()); err != nil {
			return logUpgradeError(ctx, "upgrade.tar_entry_dir_create_failed", fmt.Errorf("create directory %s: %w", targetPath, err))
		}
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return logUpgradeError(ctx, "upgrade.tar_entry_file_parent_create_failed", fmt.Errorf("create parent directory for %s: %w", targetPath, err))
		}
		out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode())
		if err != nil {
			return logUpgradeError(ctx, "upgrade.tar_entry_file_create_failed", fmt.Errorf("create file %s: %w", targetPath, err))
		}
		if _, err := io.Copy(out, tarReader); err != nil {
			_ = out.Close()
			return logUpgradeError(ctx, "upgrade.tar_entry_file_write_failed", fmt.Errorf("write file %s: %w", targetPath, err))
		}
		if err := out.Close(); err != nil {
			return logUpgradeError(ctx, "upgrade.tar_entry_file_close_failed", fmt.Errorf("close file %s: %w", targetPath, err))
		}
	case tar.TypeSymlink:
		// The archive is minisign-verified before extraction. Link checks are
		// still defense in depth against tar traversal.
		if err := safeSymlinkTarget(extractDir, targetPath, header.Linkname); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return logUpgradeError(ctx, "upgrade.tar_entry_symlink_parent_create_failed", fmt.Errorf("create parent directory for %s: %w", targetPath, err))
		}
		if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return logUpgradeError(ctx, "upgrade.tar_entry_symlink_remove_failed", fmt.Errorf("remove existing symlink path %s: %w", targetPath, err))
		}
		if err := os.Symlink(header.Linkname, targetPath); err != nil {
			return logUpgradeError(ctx, "upgrade.tar_entry_symlink_create_failed", fmt.Errorf("create symlink %s: %w", targetPath, err))
		}
	case tar.TypeLink:
		linkPath, err := safeExtractPath(ctx, extractDir, header.Linkname)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return logUpgradeError(ctx, "upgrade.tar_entry_hardlink_parent_create_failed", fmt.Errorf("create parent directory for %s: %w", targetPath, err))
		}
		if err := os.Link(linkPath, targetPath); err != nil {
			return logUpgradeError(ctx, "upgrade.tar_entry_hardlink_create_failed", fmt.Errorf("create hard link %s: %w", targetPath, err))
		}
	default:
		return fmt.Errorf("unsupported tar entry %s type %d", header.Name, header.Typeflag)
	}
	return nil
}

func safeSymlinkTarget(extractDir string, targetPath string, linkname string) error {
	if linkname == "" {
		return fmt.Errorf("tar symlink %s is missing link target", targetPath)
	}
	if filepath.IsAbs(linkname) {
		return fmt.Errorf("tar symlink %s points outside extraction root: %s", targetPath, linkname)
	}
	resolvedTarget := filepath.Clean(filepath.Join(filepath.Dir(targetPath), linkname))
	if !pathWithinRoot(extractDir, resolvedTarget) {
		return fmt.Errorf("tar symlink %s points outside extraction root: %s", targetPath, linkname)
	}
	return nil
}

func safeExtractPath(ctx context.Context, root string, name string) (string, error) {
	cleaned := filepath.Clean(name)
	if cleaned == "." {
		return "", logUpgradeError(ctx, "upgrade.tar_unsafe_path", fmt.Errorf("empty tar entry path: %w", errTarUnsafePath))
	}
	if filepath.IsAbs(cleaned) {
		return "", logUpgradeError(ctx, "upgrade.tar_unsafe_path", fmt.Errorf("tar entry %s is absolute: %w", name, errTarUnsafePath))
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
		return "", logUpgradeError(ctx, "upgrade.tar_unsafe_path", fmt.Errorf("tar entry %s escapes extraction root: %w", name, errTarUnsafePath))
	}
	targetPath := filepath.Join(root, cleaned)
	if !pathWithinRoot(root, targetPath) {
		return "", logUpgradeError(ctx, "upgrade.tar_unsafe_path", fmt.Errorf("tar entry %s escapes extraction root: %w", name, errTarUnsafePath))
	}
	return targetPath, nil
}

func pathWithinRoot(root string, targetPath string) bool {
	relativePath, err := filepath.Rel(filepath.Clean(root), filepath.Clean(targetPath))
	if err != nil {
		return false
	}
	if relativePath == "." {
		return true
	}
	return relativePath != ".." &&
		!strings.HasPrefix(relativePath, ".."+string(os.PathSeparator)) &&
		!filepath.IsAbs(relativePath)
}
