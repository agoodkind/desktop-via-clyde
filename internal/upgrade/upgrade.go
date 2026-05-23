// Package upgrade fetches the latest registered target build from the
// upstream update manifest, verifies the downloaded bundle against the
// recorded original DesignatedRequirement, swaps it into /Applications,
// and re-runs the patch flow so the new bundle launches through the
// clyde MITM proxy.
package upgrade

import (
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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	// SkipLaunchAgent is forwarded to the post-swap patch run. It is useful
	// for isolated temp-dir upgrade smokes that must not register a watcher.
	SkipLaunchAgent bool
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

// Run fetches, verifies, swaps, and re-patches the target.
func Run(t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	r := patch.NewRunner(opts.DryRun, opts.Out)
	channel := opts.Channel
	if channel == "" {
		channel = "stable"
	}

	if _, err := os.Stat(t.AppPath); err != nil {
		return fmt.Errorf("bundle not found at %s: %w", t.AppPath, err)
	}
	currentVersion, err := readBundleVersion(t)
	if err != nil {
		return err
	}
	r.Note("target=%s current version=%s channel=%s updater=%s", t.ID, currentVersion, channel, t.Updater.Kind)

	m, err := fetchManifest(t, currentVersion, channel)
	if err != nil {
		return err
	}
	r.Note("target=%s manifest: name=%s url=%s", t.ID, m.Name, m.URL)
	if m.URL == "" || m.Name == "" {
		return fmt.Errorf("manifest is missing url or name field: %+v", m)
	}
	if m.Name == currentVersion {
		r.Note("target=%s already on version %s; nothing to do", t.ID, currentVersion)
		return nil
	}

	originalDR, err := loadOriginalDR(t, opts.DryRun)
	if err != nil {
		return err
	}

	staging, err := makeStagingDir(t, m.Name)
	if err != nil {
		return err
	}
	defer func() {
		if !opts.DryRun {
			_ = os.RemoveAll(staging)
		}
	}()

	zipPath := filepath.Join(staging, archiveName(t, m.URL))
	if err := downloadZip(r, m.URL, zipPath, opts.DryRun); err != nil {
		return err
	}
	downloadSignatureVerified, err := verifyDownloadSignature(r, t, m, zipPath, opts.DryRun)
	if err != nil {
		return err
	}
	extractedApp, err := extractZip(r, zipPath, staging, t, opts.DryRun)
	if err != nil {
		return err
	}
	if err := verifyOriginalDR(r, extractedApp, originalDR, opts.DryRun); err != nil {
		return err
	}
	if err := verifyExtractedSparkleSignature(r, m, zipPath, extractedApp, downloadSignatureVerified, opts.DryRun); err != nil {
		return err
	}

	watcherLoaded := false
	if opts.SkipLaunchAgent {
		r.Note("skipping watcher unload/load")
	} else {
		watcherLoaded, err = unloadWatcher(r, opts.DryRun)
		if err != nil {
			return err
		}
	}

	if err := swapBundle(r, t, extractedApp, opts.DryRun); err != nil {
		return err
	}

	patchOpts := patch.Options{
		DryRun:            opts.DryRun,
		NoMigrateKeychain: opts.NoMigrateKeychain,
		SkipLaunchAgent:   opts.SkipLaunchAgent,
		Out:               opts.Out,
	}
	if err := patch.Patch(t, patchOpts); err != nil {
		return fmt.Errorf("re-patch after swap: %w", err)
	}

	if watcherLoaded {
		if err := loadWatcher(r, opts.DryRun); err != nil {
			r.Note("warning: failed to reload watcher LaunchAgent: %v", err)
		}
	}
	r.Note("target=%s upgrade to %s complete", t.ID, m.Name)
	return nil
}

// readBundleVersion reads CFBundleVersion from the running bundle's
// Info.plist. The updater uses this string verbatim for version comparison.
func readBundleVersion(t targets.Target) (string, error) {
	infoPath := paths.InfoPlistPath(t)
	cmd := exec.Command("/usr/bin/defaults", "read", strings.TrimSuffix(infoPath, ".plist"), "CFBundleVersion")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("read CFBundleVersion from %s: %w", infoPath, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func fetchManifest(t targets.Target, currentVersion, channel string) (updateManifest, error) {
	switch t.Updater.Kind {
	case targets.UpdaterCursorManifest:
		return fetchCursorManifest(t, currentVersion, channel)
	case targets.UpdaterSparkleAppcast:
		return fetchSparkleManifest(t)
	case targets.UpdaterClaudeSquirrel:
		return fetchClaudeSquirrelManifest(t)
	default:
		return updateManifest{}, fmt.Errorf("target %s has unsupported updater kind %q", t.ID, t.Updater.Kind)
	}
}

// fetchCursorManifest queries the Cursor update endpoint with a dummy commit
// segment. The fetch deliberately bypasses proxy configuration so the command
// behaves the same whether or not clyde is running.
func fetchCursorManifest(t targets.Target, currentVersion, channel string) (updateManifest, error) {
	const dummyCommit = "0000000000000000000000000000000000000000000000000000000000000000"
	endpoint := fmt.Sprintf(
		"https://api2.cursor.sh/updates/api/update/%s/%s/%s/%s/%s",
		url.PathEscape(t.Updater.Platform),
		url.PathEscape(t.Updater.Product),
		url.PathEscape(currentVersion),
		dummyCommit,
		url.PathEscape(channel),
	)
	body, err := fetchURL(endpoint, "Cursor/"+currentVersion, "application/json", 1<<16)
	if err != nil {
		if errors.Is(err, errNoUpdate) {
			return updateManifest{}, errors.New("upstream returned 204; no update available on this channel")
		}
		return updateManifest{}, err
	}
	return parseCursorManifest(body)
}

func fetchSparkleManifest(t targets.Target) (updateManifest, error) {
	body, err := fetchURL(t.Updater.URL, "desktop-via-clyde/upgrade", "application/xml", 2<<20)
	if err != nil {
		return updateManifest{}, err
	}
	return parseSparkleAppcast(body)
}

func fetchClaudeSquirrelManifest(t targets.Target) (updateManifest, error) {
	endpoint, err := url.Parse(t.Updater.URL)
	if err != nil {
		return updateManifest{}, fmt.Errorf("parse Claude updater URL: %w", err)
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
	body, err := fetchURL(endpoint.String(), "Claude/desktop-via-clyde", "application/json", 1<<16)
	if err != nil {
		return updateManifest{}, err
	}
	return parseClaudeSquirrelManifest(body)
}

var errNoUpdate = errors.New("no update available")

func fetchURL(endpoint, userAgent, accept string, limit int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build manifest request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", accept)
	client := directHTTPClient(60 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch manifest %s: %w", endpoint, err)
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
		return nil, fmt.Errorf("read manifest body: %w", err)
	}
	return body, nil
}

func parseCursorManifest(body []byte) (updateManifest, error) {
	m := cursorManifest{}
	if err := json.Unmarshal(body, &m); err != nil {
		return updateManifest{}, fmt.Errorf("parse Cursor manifest JSON: %w (body=%s)", err, string(body))
	}
	return updateManifest{URL: m.URL, Name: m.Name}, nil
}

func parseSparkleAppcast(body []byte) (updateManifest, error) {
	appcast := sparkleRSS{}
	if err := xml.Unmarshal(body, &appcast); err != nil {
		return updateManifest{}, fmt.Errorf("parse Sparkle appcast XML: %w", err)
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
	return updateManifest{}, errors.New("Sparkle appcast contains no arm64 full zip enclosure")
}

func parseClaudeSquirrelManifest(body []byte) (updateManifest, error) {
	m := claudeSquirrelManifest{}
	if err := json.Unmarshal(body, &m); err != nil {
		return updateManifest{}, fmt.Errorf("parse Claude Squirrel manifest JSON: %w (body=%s)", err, string(body))
	}
	for _, release := range m.Releases {
		if release.UpdateTo.URL == "" {
			continue
		}
		name := firstNonEmpty(release.UpdateTo.Version, release.Version, strings.TrimPrefix(release.UpdateTo.Name, "Claude "))
		return updateManifest{URL: release.UpdateTo.URL, Name: name}, nil
	}
	return updateManifest{}, errors.New("Claude Squirrel manifest contains no updateTo.url")
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
		return "", fmt.Errorf("generate nonsecret updater device id: %w", err)
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
func loadOriginalDR(t targets.Target, dryRun bool) (string, error) {
	return patch.OriginalDesignatedRequirement(t, !dryRun)
}

// makeStagingDir creates an isolated directory under the state root for the
// download and extraction. The directory is removed on return from Run.
func makeStagingDir(t targets.Target, version string) (string, error) {
	root := filepath.Join(paths.StateRoot(), "upgrade-staging", t.ID+"-"+version+"-"+strconv.FormatInt(time.Now().Unix(), 10))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create staging dir %s: %w", root, err)
	}
	return root, nil
}

// downloadZip streams the manifest URL to zipPath. It bypasses proxy
// configuration for the same reason the manifest fetch does.
func downloadZip(r *patch.Runner, srcURL, zipPath string, dryRun bool) error {
	r.Note("downloading %s -> %s", srcURL, zipPath)
	if dryRun {
		return nil
	}
	req, err := http.NewRequest(http.MethodGet, srcURL, http.NoBody)
	if err != nil {
		return fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("User-Agent", "desktop-via-clyde/upgrade")
	req.Header.Set("Accept", "application/zip")
	client := directHTTPClient(30 * time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", srcURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("download status %d for %s", resp.StatusCode, srcURL)
	}
	out, err := os.Create(zipPath)
	if err != nil {
		return fmt.Errorf("create zip file: %w", err)
	}
	defer func() { _ = out.Close() }()
	n, err := io.Copy(out, resp.Body)
	if err != nil {
		return fmt.Errorf("write zip body: %w", err)
	}
	r.Note("downloaded %d bytes", n)
	return nil
}

func verifyDownloadSignature(r *patch.Runner, t targets.Target, m updateManifest, zipPath string, dryRun bool) (bool, error) {
	if m.Signature == "" {
		return true, nil
	}
	r.Note("target=%s verifying Sparkle Ed25519 signature for %s", t.ID, zipPath)
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
	r.Note("target=%s Sparkle signature did not match the current public key; will allow key rotation only after original DR verification", t.ID)
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
	r.Note("verifying Sparkle Ed25519 signature with extracted bundle public key after DR match")
	if dryRun {
		return nil
	}
	info, err := patch.ReadInfoPlist(filepath.Join(extractedApp, "Contents", "Info.plist"))
	if err != nil {
		return fmt.Errorf("read extracted bundle Info.plist for Sparkle key rotation: %w", err)
	}
	if info.SUPublicEDKey == "" {
		return fmt.Errorf("Sparkle signature did not match current key and extracted bundle has no SUPublicEDKey")
	}
	ok, err := verifySparkleSignature(zipPath, info.SUPublicEDKey, m.Signature)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("Sparkle Ed25519 signature verification failed for %s with current and extracted bundle public keys", zipPath)
	}
	return nil
}

func verifySparkleSignature(zipPath, publicKeyBase64, signatureBase64 string) (bool, error) {
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKeyBase64))
	if err != nil {
		return false, fmt.Errorf("decode Sparkle public key: %w", err)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return false, fmt.Errorf("Sparkle public key has %d bytes, want %d", len(publicKey), ed25519.PublicKeySize)
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signatureBase64))
	if err != nil {
		return false, fmt.Errorf("decode Sparkle signature: %w", err)
	}
	if len(signature) != ed25519.SignatureSize {
		return false, fmt.Errorf("Sparkle signature has %d bytes, want %d", len(signature), ed25519.SignatureSize)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return false, fmt.Errorf("read zip for Sparkle signature verification: %w", err)
	}
	return ed25519.Verify(ed25519.PublicKey(publicKey), data, signature), nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
func extractZip(r *patch.Runner, zipPath, staging string, t targets.Target, dryRun bool) (string, error) {
	extractDir := filepath.Join(staging, "extracted")
	if !dryRun {
		if err := os.MkdirAll(extractDir, 0o755); err != nil {
			return "", fmt.Errorf("create extract dir: %w", err)
		}
	}
	if err := r.Run("/usr/bin/ditto", "-x", "-k", zipPath, extractDir); err != nil {
		return "", fmt.Errorf("ditto -x -k: %w", err)
	}
	expected := filepath.Join(extractDir, filepath.Base(t.AppPath))
	if dryRun {
		return expected, nil
	}
	if _, err := os.Stat(expected); err != nil {
		return "", fmt.Errorf("expected %s inside zip, not found: %w", filepath.Base(t.AppPath), err)
	}
	return expected, nil
}

// verifyOriginalDR runs codesign --verify --deep --strict against the
// extracted bundle, requiring the recorded upstream DR to match.
func verifyOriginalDR(r *patch.Runner, appPath, dr string, dryRun bool) error {
	r.Note("verifying %s satisfies original DR", appPath)
	if dryRun {
		return nil
	}
	return r.Run("/usr/bin/codesign", "--verify", "--deep", "--strict", "-R="+dr, appPath)
}

// unloadWatcher boots out the watcher LaunchAgent so its FSEvents callback
// does not fire a concurrent patch flow while the app is mid-swap.
func unloadWatcher(r *patch.Runner, dryRun bool) (bool, error) {
	plist := paths.LaunchAgentPlist()
	if _, err := os.Stat(plist); err != nil {
		r.Note("watcher LaunchAgent plist not present at %s; nothing to unload", plist)
		return false, nil
	}
	uid := strconv.Itoa(os.Getuid())
	r.Note("unloading watcher LaunchAgent")
	if dryRun {
		return true, nil
	}
	if err := exec.Command("/bin/launchctl", "bootout", "gui/"+uid, plist).Run(); err != nil {
		r.Note("watcher bootout returned %v (was the agent loaded?); continuing", err)
	}
	return true, nil
}

// loadWatcher re-bootstraps the watcher LaunchAgent so post-update drift
// detection keeps working on the new bundle.
func loadWatcher(r *patch.Runner, dryRun bool) error {
	plist := paths.LaunchAgentPlist()
	if _, err := os.Stat(plist); err != nil {
		return nil
	}
	uid := strconv.Itoa(os.Getuid())
	r.Note("reloading watcher LaunchAgent")
	if dryRun {
		return nil
	}
	return exec.Command("/bin/launchctl", "bootstrap", "gui/"+uid, plist).Run()
}

// swapBundle removes the existing /Applications/<App>.app and the stale
// desktop-via-clyde backup, then installs the freshly extracted upstream copy.
func swapBundle(r *patch.Runner, t targets.Target, extractedApp string, dryRun bool) error {
	r.Note("removing patched bundle %s and stale backup %s", t.AppPath, paths.BackupDir(t))
	if !dryRun {
		if err := os.RemoveAll(t.AppPath); err != nil {
			return fmt.Errorf("remove %s: %w", t.AppPath, err)
		}
		if err := os.RemoveAll(paths.BackupDir(t)); err != nil {
			return fmt.Errorf("remove %s: %w", paths.BackupDir(t), err)
		}
	}
	r.Note("installing fresh bundle %s -> %s", extractedApp, t.AppPath)
	return r.Run("/usr/bin/ditto", extractedApp, t.AppPath)
}
