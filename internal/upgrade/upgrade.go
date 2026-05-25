// Package upgrade fetches the latest registered target build from the
// upstream update manifest, verifies the downloaded bundle against the
// recorded original DesignatedRequirement, swaps it into /Applications,
// and re-runs the patch flow so the new bundle launches through the
// clyde MITM proxy.
package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// Options controls one upgrade invocation.
type Options struct {
	// Channel selects the upstream release channel. Empty defaults to "stable".
	// Cursor uses this value directly. Codex and Claude ignore it because their
	// endpoints encode channel in the target metadata.
	Channel string
	// DryRun prints every step without modifying the bundle or the filesystem.
	DryRun bool
	// NoMigrateKeychain is forwarded to the post-swap patch run.
	NoMigrateKeychain bool
	// Out receives progress output. Defaults to os.Stdout.
	Out io.Writer
}

type updateManifest struct {
	URL       string
	Name      string
	Signature string
}

type cursorManifest struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

type sparkleRSS struct {
	Channel sparkleChannel `xml:"channel"`
}

type sparkleChannel struct {
	Items []sparkleItem `xml:"item"`
}

type sparkleItem struct {
	Title                string           `xml:"title"`
	Version              string           `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle version"`
	ShortVersionString   string           `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle shortVersionString"`
	HardwareRequirements string           `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle hardwareRequirements"`
	Enclosure            sparkleEnclosure `xml:"enclosure"`
}

type sparkleEnclosure struct {
	URL       string `xml:"url,attr"`
	Length    string `xml:"length,attr"`
	Type      string `xml:"type,attr"`
	Signature string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle edSignature,attr"`
}

type claudeSquirrelManifest struct {
	CurrentRelease string                  `json:"currentRelease"`
	Releases       []claudeSquirrelRelease `json:"releases"`
}

type claudeSquirrelRelease struct {
	Version  string               `json:"version"`
	UpdateTo claudeSquirrelUpdate `json:"updateTo"`
}

type claudeSquirrelUpdate struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	URL     string `json:"url"`
}

var readDesignatedRequirement = patch.DesignatedRequirement

// Run fetches, verifies, swaps, and re-patches the target.
func Run(ctx context.Context, t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	upgradeLog.InfoContext(ctx, "upgrade.start", "target", t.ID, "app_path", t.AppPath, "dry_run", opts.DryRun)
	r := patch.NewRunner(ctx, opts.DryRun, opts.Out)
	channel := opts.Channel
	if channel == "" {
		channel = "stable"
	}

	if _, err := os.Stat(t.AppPath); err != nil {
		return logUpgradeError(ctx, "upgrade.bundle_stat_failed", fmt.Errorf("bundle not found at %s: %w", t.AppPath, err))
	}
	currentVersion, err := readBundleVersion(ctx, t)
	if err != nil {
		return err
	}
	notef(r, fmt.Sprintf("target=%s current version=%s channel=%s updater=%s", t.ID, currentVersion, channel, t.Updater.Kind))

	m, err := fetchManifest(ctx, t, currentVersion, channel)
	if err != nil {
		return err
	}
	notef(r, fmt.Sprintf("target=%s manifest: name=%s url=%s", t.ID, m.Name, m.URL))
	if m.URL == "" || m.Name == "" {
		return logUpgradeError(ctx, "upgrade.manifest_missing_fields", fmt.Errorf("manifest is missing url or name field: %+v", m))
	}
	if m.Name == currentVersion {
		if err := handleCurrentVersion(ctx, r, t, currentVersion, opts); err != nil {
			return err
		}
		return nil
	}

	originalDR, err := loadOriginalDR(ctx, t, opts.DryRun)
	if err != nil {
		return err
	}

	staging, err := makeStagingDir(ctx, t, m.Name)
	if err != nil {
		return err
	}
	defer func() {
		if !opts.DryRun {
			_ = os.RemoveAll(staging)
		}
	}()

	zipPath := filepath.Join(staging, archiveName(t, m.URL))
	if err := downloadZip(ctx, r, m.URL, zipPath, opts.DryRun); err != nil {
		return err
	}
	downloadSignatureVerified, err := verifyDownloadSignature(r, t, m, zipPath, opts.DryRun)
	if err != nil {
		return err
	}
	extractedApp, err := extractZip(ctx, r, zipPath, staging, t, opts.DryRun)
	if err != nil {
		return err
	}
	if err := verifyOriginalDR(ctx, r, extractedApp, originalDR, opts.DryRun); err != nil {
		return err
	}
	if err := verifyExtractedSparkleSignature(r, m, zipPath, extractedApp, downloadSignatureVerified, opts.DryRun); err != nil {
		return err
	}

	if err := swapBundle(ctx, r, t, extractedApp, opts.DryRun); err != nil {
		return err
	}

	patchOpts := patch.Options{
		DryRun:            opts.DryRun,
		NoMigrateKeychain: opts.NoMigrateKeychain,
		Out:               opts.Out,
	}
	if err := patch.Patch(ctx, t, patchOpts); err != nil {
		return logUpgradeError(ctx, "upgrade.repatch_failed", fmt.Errorf("re-patch after swap: %w", err))
	}

	notef(r, fmt.Sprintf("target=%s upgrade to %s complete", t.ID, m.Name))
	return nil
}

func notef(r *patch.Runner, message string) {
	prefix := "[run]"
	if r.DryRun {
		prefix = "[dry-run]"
	}
	fmt.Fprintf(r.Out, "%s %s\n", prefix, message)
}

// readBundleVersion reads CFBundleVersion from the running bundle's
// Info.plist. The updater uses this string verbatim for version comparison.
func readBundleVersion(ctx context.Context, t targets.Target) (string, error) {
	info, err := patch.ReadInfoPlist(paths.InfoPlistPath(t))
	if err != nil {
		return "", logUpgradeError(ctx, "upgrade.bundle_version_read_failed", fmt.Errorf("read CFBundleVersion from %s: %w", paths.InfoPlistPath(t), err))
	}
	return strings.TrimSpace(info.CFBundleVersion), nil
}

func fetchManifest(ctx context.Context, t targets.Target, currentVersion, channel string) (updateManifest, error) {
	switch t.Updater.Kind {
	case targets.UpdaterCursorManifest:
		return fetchCursorManifest(ctx, t, currentVersion, channel)
	case targets.UpdaterSparkleAppcast:
		return fetchSparkleManifest(ctx, t)
	case targets.UpdaterClaudeSquirrel:
		return fetchClaudeSquirrelManifest(ctx, t)
	default:
		return updateManifest{}, fmt.Errorf("target %s has unsupported updater kind %q", t.ID, t.Updater.Kind)
	}
}

// fetchCursorManifest queries the Cursor update endpoint with a dummy commit
// segment. The fetch deliberately bypasses proxy configuration so the command
// behaves the same whether or not clyde is running.
func fetchCursorManifest(ctx context.Context, t targets.Target, currentVersion, channel string) (updateManifest, error) {
	const dummyCommit = "0000000000000000000000000000000000000000000000000000000000000000"
	endpoint := fmt.Sprintf(
		"https://api2.cursor.sh/updates/api/update/%s/%s/%s/%s/%s",
		url.PathEscape(t.Updater.Platform),
		url.PathEscape(t.Updater.Product),
		url.PathEscape(currentVersion),
		dummyCommit,
		url.PathEscape(channel),
	)
	body, err := fetchURL(ctx, endpoint, "Cursor/"+currentVersion, "application/json", 1<<16)
	if err != nil {
		if errors.Is(err, errNoUpdate) {
			return updateManifest{}, errors.New("upstream returned 204; no update available on this channel")
		}
		return updateManifest{}, err
	}
	return parseCursorManifest(body)
}

func fetchSparkleManifest(ctx context.Context, t targets.Target) (updateManifest, error) {
	body, err := fetchURL(ctx, t.Updater.URL, "desktop-via-clyde/upgrade", "application/xml", 2<<20)
	if err != nil {
		return updateManifest{}, err
	}
	return parseSparkleAppcast(body)
}

func fetchClaudeSquirrelManifest(ctx context.Context, t targets.Target) (updateManifest, error) {
	endpoint, err := url.Parse(t.Updater.URL)
	if err != nil {
		return updateManifest{}, logUpgradeError(ctx, "upgrade.claude_updater_url_parse_failed", fmt.Errorf("parse Claude updater URL: %w", err))
	}
	deviceID, err := generatedDeviceID()
	if err != nil {
		return updateManifest{}, err
	}
	paramName := t.Updater.DeviceIDParamName
	if paramName == "" {
		paramName = "device_id"
	}
	values := endpoint.Query()
	values.Set(paramName, deviceID)
	endpoint.RawQuery = values.Encode()
	body, err := fetchURL(ctx, endpoint.String(), "Claude/desktop-via-clyde", "application/json", 1<<16)
	if err != nil {
		return updateManifest{}, err
	}
	return parseClaudeSquirrelManifest(body)
}

var errNoUpdate = errors.New("no update available")

func fetchURL(ctx context.Context, endpoint, userAgent, accept string, limit int64) ([]byte, error) {
	upgradeLog.DebugContext(ctx, "upgrade.fetch_url.boundary", "endpoint", endpoint, "accept", accept, "limit", limit)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, logUpgradeError(ctx, "upgrade.manifest_request_build_failed", fmt.Errorf("build manifest request: %w", err))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	client := directHTTPClient(60 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, logUpgradeError(ctx, "upgrade.manifest_fetch_failed", fmt.Errorf("fetch manifest %s: %w", endpoint, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNoContent {
		return nil, errNoUpdate
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("manifest status %d: %s", resp.StatusCode, string(body))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, logUpgradeError(ctx, "upgrade.manifest_body_read_failed", fmt.Errorf("read manifest body: %w", err))
	}
	return body, nil
}

func parseCursorManifest(body []byte) (updateManifest, error) {
	m := cursorManifest{URL: "", Name: ""}
	if err := json.Unmarshal(body, &m); err != nil {
		return updateManifest{}, logUpgradeErrorNoContext("upgrade.cursor_manifest_parse_failed", fmt.Errorf("parse Cursor manifest JSON: %w (body=%s)", err, string(body)))
	}
	return updateManifest{URL: m.URL, Name: m.Name, Signature: ""}, nil
}

func parseSparkleAppcast(body []byte) (updateManifest, error) {
	appcast := sparkleRSS{Channel: sparkleChannel{Items: nil}}
	if err := xml.Unmarshal(body, &appcast); err != nil {
		return updateManifest{}, logUpgradeErrorNoContext("upgrade.sparkle_appcast_parse_failed", fmt.Errorf("parse Sparkle appcast XML: %w", err))
	}
	for _, item := range appcast.Channel.Items {
		if item.Enclosure.URL == "" {
			continue
		}
		if item.HardwareRequirements != "" && !strings.Contains(strings.ToLower(item.HardwareRequirements), "arm64") {
			continue
		}
		name := firstNonEmpty(item.Version, item.ShortVersionString, item.Title)
		return updateManifest{
			URL:       item.Enclosure.URL,
			Name:      name,
			Signature: item.Enclosure.Signature,
		}, nil
	}
	return updateManifest{}, errors.New("sparkle appcast contains no arm64 full zip enclosure")
}

func parseClaudeSquirrelManifest(body []byte) (updateManifest, error) {
	m := claudeSquirrelManifest{CurrentRelease: "", Releases: nil}
	if err := json.Unmarshal(body, &m); err != nil {
		return updateManifest{}, logUpgradeErrorNoContext("upgrade.claude_squirrel_manifest_parse_failed", fmt.Errorf("parse Claude Squirrel manifest JSON: %w (body=%s)", err, string(body)))
	}
	for _, release := range m.Releases {
		if release.UpdateTo.URL == "" {
			continue
		}
		name := firstNonEmpty(release.UpdateTo.Version, release.Version, strings.TrimPrefix(release.UpdateTo.Name, "Claude "))
		return updateManifest{URL: release.UpdateTo.URL, Name: name, Signature: ""}, nil
	}
	return updateManifest{}, errors.New("claude squirrel manifest contains no updateTo.url")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func generatedDeviceID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", logUpgradeErrorNoContext("upgrade.device_id_generate_failed", fmt.Errorf("generate nonsecret updater device id: %w", err))
	}
	return "desktop-via-clyde-" + hex.EncodeToString(data[:]), nil
}

func directHTTPClient(timeout time.Duration) *http.Client {
	directTransport := &http.Transport{
		Proxy:               nil,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 30 * time.Second,
	}
	return &http.Client{
		Transport: directTransport,
		Timeout:   timeout,
	}
}

// loadOriginalDR reads or repairs the recorded upstream DR string from
// state.json. The field is populated at first patch time by reading
// codesign --display --requirements - against the unmodified upstream bundle.
func loadOriginalDR(ctx context.Context, t targets.Target, dryRun bool) (string, error) {
	dr, err := patch.OriginalDesignatedRequirement(ctx, t, !dryRun)
	if err == nil {
		return verifyOriginalRequirementIsUpstream(t, dr)
	}
	if t.ID != "claude" || !errors.Is(err, patch.ErrMissingStateEntry) {
		return "", logUpgradeError(ctx, "upgrade.original_dr_load_failed", fmt.Errorf("load original designated requirement: %w", err))
	}
	return bootstrapClaudeOriginalDR(ctx, t)
}

func bootstrapClaudeOriginalDR(ctx context.Context, t targets.Target) (string, error) {
	realPath := paths.RealBinaryPath(t)
	if _, err := os.Stat(realPath); err == nil {
		return "", logUpgradeError(ctx, "upgrade.claude_state_missing_real_exists", fmt.Errorf("target=claude has no state entry but %s exists; restore or unpatch Claude before upgrade", realPath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", logUpgradeError(ctx, "upgrade.claude_real_stat_failed", fmt.Errorf("stat %s: %w", realPath, err))
	}
	dr, err := readDesignatedRequirement(ctx, paths.MainBinaryPath(t))
	if err != nil {
		return "", logUpgradeError(ctx, "upgrade.claude_original_dr_capture_failed", fmt.Errorf("capture Claude DesignatedRequirement from clean app: %w", err))
	}
	return verifyOriginalRequirementIsUpstream(t, dr)
}

func verifyOriginalRequirementIsUpstream(t targets.Target, dr string) (string, error) {
	if strings.Contains(dr, paths.SignTeamID) {
		return "", logUpgradeErrorNoContext("upgrade.original_dr_identifies_local_team", fmt.Errorf("target=%s DesignatedRequirement identifies local signing team %s, not upstream", t.ID, paths.SignTeamID))
	}
	return dr, nil
}

func isPatchedBundle(t targets.Target) (bool, error) {
	realPath := paths.RealBinaryPath(t)
	_, err := os.Stat(realPath)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, logUpgradeErrorNoContext("upgrade.patched_bundle_stat_failed", fmt.Errorf("stat %s: %w", realPath, err))
}

func handleCurrentVersion(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	currentVersion string,
	opts Options,
) error {
	patched, err := isPatchedBundle(t)
	if err != nil {
		return err
	}
	if !patched {
		notef(r, fmt.Sprintf("target=%s already on version %s; patching clean bundle", t.ID, currentVersion))
		if err := patch.Patch(ctx, t, patch.Options{
			DryRun:            opts.DryRun,
			NoMigrateKeychain: opts.NoMigrateKeychain,
			Out:               opts.Out,
		}); err != nil {
			return logUpgradeError(ctx, "upgrade.current_version_patch_failed", fmt.Errorf("patch clean bundle after version check: %w", err))
		}
		return nil
	}
	if _, err := loadOriginalDR(ctx, t, opts.DryRun); err != nil {
		return err
	}
	notef(r, fmt.Sprintf("target=%s already on version %s; nothing to do", t.ID, currentVersion))
	return nil
}

// makeStagingDir creates an isolated directory under the state root for the
// download and extraction. The directory is removed on return from Run.
func makeStagingDir(ctx context.Context, t targets.Target, version string) (string, error) {
	root := filepath.Join(paths.StateRoot(), "upgrade-staging", t.ID+"-"+version+"-"+strconv.FormatInt(clock.Now().Unix(), 10))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", logUpgradeError(ctx, "upgrade.staging_dir_create_failed", fmt.Errorf("create staging dir %s: %w", root, err))
	}
	return root, nil
}

// downloadZip streams the manifest URL to zipPath. It bypasses proxy
// configuration for the same reason the manifest fetch does.
func downloadZip(ctx context.Context, r *patch.Runner, srcURL, zipPath string, dryRun bool) error {
	upgradeLog.DebugContext(ctx, "upgrade.download_zip.boundary", "url", srcURL, "zip_path", zipPath, "dry_run", dryRun)
	notef(r, fmt.Sprintf("downloading %s -> %s", srcURL, zipPath))
	if dryRun {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srcURL, http.NoBody)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.download_request_build_failed", fmt.Errorf("build download request: %w", err))
	}
	req.Header.Set("User-Agent", "desktop-via-clyde/upgrade")
	req.Header.Set("Accept", "application/zip")
	client := directHTTPClient(30 * time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.download_failed", fmt.Errorf("download %s: %w", srcURL, err))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("download status %d for %s", resp.StatusCode, srcURL)
	}
	out, err := os.Create(zipPath)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.download_zip_create_failed", fmt.Errorf("create zip file: %w", err))
	}
	defer func() { _ = out.Close() }()
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.download_zip_write_failed", fmt.Errorf("write zip body: %w", err))
	}
	notef(r, fmt.Sprintf("downloaded %d bytes", n))
	return nil
}

func verifyDownloadSignature(r *patch.Runner, t targets.Target, m updateManifest, zipPath string, dryRun bool) (bool, error) {
	if m.Signature == "" {
		return true, nil
	}
	notef(r, fmt.Sprintf("target=%s verifying Sparkle Ed25519 signature for %s", t.ID, zipPath))
	if dryRun {
		return true, nil
	}
	publicKeys, err := currentSparklePublicKeys(t)
	if err != nil {
		return false, err
	}
	for _, publicKey := range publicKeys {
		ok, err := verifySparkleSignature(zipPath, publicKey, m.Signature)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	notef(r, fmt.Sprintf("target=%s Sparkle signature did not match the current public key; will allow key rotation only after original DR verification", t.ID))
	return false, nil
}

func currentSparklePublicKeys(t targets.Target) ([]string, error) {
	keys := make([]string, 0, 2)
	if info, err := patch.ReadInfoPlist(paths.InfoPlistPath(t)); err == nil && info.SUPublicEDKey != "" {
		keys = append(keys, info.SUPublicEDKey)
	}
	if t.Updater.SparklePublicKey != "" && !containsString(keys, t.Updater.SparklePublicKey) {
		keys = append(keys, t.Updater.SparklePublicKey)
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("target %s has no Sparkle public key in Info.plist or target metadata", t.ID)
	}
	return keys, nil
}

func verifyExtractedSparkleSignature(r *patch.Runner, m updateManifest, zipPath, extractedApp string, alreadyVerified bool, dryRun bool) error {
	if m.Signature == "" || alreadyVerified {
		return nil
	}
	notef(r, "verifying Sparkle Ed25519 signature with extracted bundle public key after DR match")
	if dryRun {
		return nil
	}
	info, err := patch.ReadInfoPlist(filepath.Join(extractedApp, "Contents", "Info.plist"))
	if err != nil {
		return logUpgradeErrorNoContext("upgrade.extracted_sparkle_info_read_failed", fmt.Errorf("read extracted bundle Info.plist for Sparkle key rotation: %w", err))
	}
	if info.SUPublicEDKey == "" {
		return fmt.Errorf("sparkle signature did not match current key and extracted bundle has no SUPublicEDKey")
	}
	ok, err := verifySparkleSignature(zipPath, info.SUPublicEDKey, m.Signature)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("sparkle Ed25519 signature verification failed for %s with current and extracted bundle public keys", zipPath)
	}
	return nil
}

func verifySparkleSignature(zipPath, publicKeyBase64, signatureBase64 string) (bool, error) {
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil {
		return false, logUpgradeErrorNoContext("upgrade.sparkle_public_key_decode_failed", fmt.Errorf("decode Sparkle public key: %w", err))
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return false, fmt.Errorf("sparkle public key has %d bytes, want %d", len(publicKey), ed25519.PublicKeySize)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signatureBase64))
	if err != nil {
		return false, logUpgradeErrorNoContext("upgrade.sparkle_signature_decode_failed", fmt.Errorf("decode Sparkle signature: %w", err))
	}
	if len(signature) != ed25519.SignatureSize {
		return false, fmt.Errorf("sparkle signature has %d bytes, want %d", len(signature), ed25519.SignatureSize)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return false, logUpgradeErrorNoContext("upgrade.sparkle_zip_read_failed", fmt.Errorf("read zip for Sparkle signature verification: %w", err))
	}
	return ed25519.Verify(ed25519.PublicKey(publicKey), data, signature), nil
}

func containsString(values []string, target string) bool {
	return slices.Contains(values, target)
}

func archiveName(t targets.Target, srcURL string) string {
	parsed, err := url.Parse(srcURL)
	if err == nil {
		base := filepath.Base(parsed.Path)
		if base != "." && base != "/" && base != "" {
			return base
		}
	}
	return t.ID + ".zip"
}

// extractZip unpacks the zip via /usr/bin/ditto which preserves bundle
// metadata that archive/zip would mangle. Returns the extracted <Target>.app.
func extractZip(ctx context.Context, r *patch.Runner, zipPath, staging string, t targets.Target, dryRun bool) (string, error) {
	extractDir := filepath.Join(staging, "extracted")
	if !dryRun {
		if err := os.MkdirAll(extractDir, 0o755); err != nil {
			return "", logUpgradeError(ctx, "upgrade.extract_dir_create_failed", fmt.Errorf("create extract dir: %w", err))
		}
	}
	if err := r.Run(ctx, "/usr/bin/ditto", "-x", "-k", zipPath, extractDir); err != nil {
		return "", logUpgradeError(ctx, "upgrade.ditto_extract_failed", fmt.Errorf("ditto -x -k: %w", err))
	}
	expected := filepath.Join(extractDir, filepath.Base(t.AppPath))
	if dryRun {
		return expected, nil
	}
	if _, err := os.Stat(expected); err != nil {
		return "", logUpgradeError(ctx, "upgrade.extracted_app_stat_failed", fmt.Errorf("expected %s inside zip, not found: %w", filepath.Base(t.AppPath), err))
	}
	return expected, nil
}

// verifyOriginalDR runs codesign --verify --deep --strict against the
// extracted bundle, requiring the recorded upstream DR to match.
func verifyOriginalDR(ctx context.Context, r *patch.Runner, appPath, dr string, dryRun bool) error {
	notef(r, fmt.Sprintf("verifying %s satisfies original DR", appPath))
	if dryRun {
		return nil
	}
	if err := r.Run(ctx, "/usr/bin/codesign", "--verify", "--deep", "--strict", "-R="+dr, appPath); err != nil {
		return logUpgradeError(ctx, "upgrade.original_dr_verify_failed", fmt.Errorf("verify original DR for %s: %w", appPath, err))
	}
	return nil
}

// swapBundle removes the existing /Applications/<App>.app and the stale
// desktop-via-clyde backup, then installs the freshly extracted upstream copy.
func swapBundle(ctx context.Context, r *patch.Runner, t targets.Target, extractedApp string, dryRun bool) error {
	upgradeLog.DebugContext(ctx, "upgrade.swap_bundle.boundary", "target", t.ID, "app_path", t.AppPath, "extracted_app", extractedApp, "dry_run", dryRun)
	notef(r, fmt.Sprintf("removing patched bundle %s and stale backup %s", t.AppPath, paths.BackupDir(t)))
	if !dryRun {
		if err := os.RemoveAll(t.AppPath); err != nil {
			return logUpgradeError(ctx, "upgrade.remove_current_bundle_failed", fmt.Errorf("remove %s: %w", t.AppPath, err))
		}
		if err := os.RemoveAll(paths.BackupDir(t)); err != nil {
			return logUpgradeError(ctx, "upgrade.remove_backup_failed", fmt.Errorf("remove %s: %w", paths.BackupDir(t), err))
		}
	}
	notef(r, fmt.Sprintf("installing fresh bundle %s -> %s", extractedApp, t.AppPath))
	if err := r.Run(ctx, "/usr/bin/ditto", extractedApp, t.AppPath); err != nil {
		return logUpgradeError(ctx, "upgrade.install_fresh_bundle_failed", fmt.Errorf("install fresh bundle %s -> %s: %w", extractedApp, t.AppPath, err))
	}
	return nil
}
