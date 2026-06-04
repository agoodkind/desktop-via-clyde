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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"goodkind.io/desktop-via-clyde/internal/catalog"
	"goodkind.io/desktop-via-clyde/internal/clock"
	"goodkind.io/desktop-via-clyde/internal/extensions"
	"goodkind.io/desktop-via-clyde/internal/operations"
	"goodkind.io/desktop-via-clyde/internal/patch"
	"goodkind.io/desktop-via-clyde/internal/paths"
	"goodkind.io/desktop-via-clyde/internal/targets"
)

// BootstrapStrategy recovers an upstream designated requirement when state is missing.
type BootstrapStrategy func(context.Context, targets.Target) (string, error)

var (
	bootstrapStrategiesMu sync.RWMutex
	bootstrapStrategies   = map[string]BootstrapStrategy{}
)

const (
	// AppUpgradeCapability is the operation capability for app upgrades.
	AppUpgradeCapability = "app.upgrade"
	// CleanMainBinaryBootstrapCapability recovers original requirements from a clean main binary.
	CleanMainBinaryBootstrapCapability = "clean-main-binary"
)

// RegisterOperations links upgrade-owned operation capabilities.
func RegisterOperations() error {
	if !catalog.HasOperationCapability(AppUpgradeCapability) {
		if err := catalog.RegisterOperationCapability(AppUpgradeCapability); err != nil {
			return logUpgradeRegistrationError("register upgrade capability", err)
		}
	}
	if err := operations.Register(AppUpgradeCapability, Operation); err != nil {
		return logUpgradeRegistrationError("register upgrade operation", err)
	}
	return nil
}

// RegisterBootstrapStrategies links upgrade bootstrap strategies.
func RegisterBootstrapStrategies() error {
	if !catalog.HasBootstrapCapability(CleanMainBinaryBootstrapCapability) {
		if err := catalog.RegisterBootstrapCapability(CleanMainBinaryBootstrapCapability); err != nil {
			return logUpgradeRegistrationError("register clean-main-binary bootstrap capability", err)
		}
	}
	if err := RegisterBootstrapStrategy(CleanMainBinaryBootstrapCapability, bootstrapOriginalDRFromCleanMainBinary); err != nil {
		return logUpgradeRegistrationError("register clean-main-binary bootstrap strategy", err)
	}
	return nil
}

// RegisterValidators links upgrade config validation.
func RegisterValidators() error {
	if err := extensions.RegisterAppValidator("original_dr_bootstrap_capability", extensions.ValidateOriginalDRBootstrapCapability); err != nil {
		return logUpgradeRegistrationError("register original DR bootstrap validator", err)
	}
	return nil
}

// RegisterBootstrapStrategy links one bootstrap capability to its strategy.
func RegisterBootstrapStrategy(capability string, strategy BootstrapStrategy) error {
	if !catalog.HasBootstrapCapability(capability) {
		return fmt.Errorf("bootstrap capability %q is not linked", capability)
	}
	if strategy == nil {
		return fmt.Errorf("bootstrap capability %q strategy is required", capability)
	}
	bootstrapStrategiesMu.Lock()
	defer bootstrapStrategiesMu.Unlock()
	if _, ok := bootstrapStrategies[capability]; ok {
		return fmt.Errorf("bootstrap capability %q strategy is already registered", capability)
	}
	bootstrapStrategies[capability] = strategy
	return nil
}

// RegisteredBootstrapStrategies returns bootstrap capabilities with strategies.
func RegisteredBootstrapStrategies() []string {
	bootstrapStrategiesMu.RLock()
	defer bootstrapStrategiesMu.RUnlock()
	names := make([]string, 0, len(bootstrapStrategies))
	for name := range bootstrapStrategies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func lookupBootstrapStrategy(capability string) (BootstrapStrategy, bool) {
	bootstrapStrategiesMu.RLock()
	defer bootstrapStrategiesMu.RUnlock()
	strategy, ok := bootstrapStrategies[capability]
	return strategy, ok
}

// Options controls one upgrade invocation.
type Options struct {
	// Channel selects the upstream release channel when the target updater
	// declares channels.
	Channel string
	// DryRun prints every step without modifying the bundle or the filesystem.
	DryRun bool
	// MigrateKeychain is forwarded to the post-swap patch run.
	MigrateKeychain bool
	// Out receives progress output. Defaults to os.Stdout.
	Out io.Writer
	// LogOut receives raw subprocess output. Defaults to Out.
	LogOut io.Writer
}

// Operation runs the app upgrade operation for one configured target.
func Operation(ctx context.Context, req operations.Request) error {
	if req.App == nil {
		return fmt.Errorf("%s requires an app target", req.Capability)
	}
	if err := Run(ctx, *req.App, Options{
		Channel:         req.Flags.String("channel"),
		DryRun:          req.Flags.Bool("dry-run"),
		MigrateKeychain: req.Flags.Bool("migrate-keychain"),
		Out:             req.Out,
		LogOut:          req.LogOut,
	}); err != nil {
		upgradeLog.ErrorContext(ctx, "upgrade.operation_failed", "err", err)
		return fmt.Errorf("upgrade operation: %w",
			operations.Error(ctx, "operations.upgrade_failed", "upgrade app", err))
	}
	return nil
}

type updateManifest struct {
	URL       string
	Name      string
	Signature string
}

type pathJSONManifest struct {
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

type squirrelJSONManifest struct {
	CurrentRelease string                `json:"currentRelease"`
	Releases       []squirrelJSONRelease `json:"releases"`
}

type squirrelJSONRelease struct {
	Version  string             `json:"version"`
	UpdateTo squirrelJSONUpdate `json:"updateTo"`
}

type squirrelJSONUpdate struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	URL     string `json:"url"`
}

var readDesignatedRequirement = patch.DesignatedRequirement

const missingBundleCurrentVersion = "0.0.0"

type bundleVersionState struct {
	CurrentVersion string
	Missing        bool
}

// Run fetches, verifies, swaps, and re-patches the target.
func Run(ctx context.Context, t targets.Target, opts Options) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	upgradeLog.InfoContext(ctx, "upgrade.start", "target", t.ID, "app_path", t.AppPath, "dry_run", opts.DryRun)
	r := patch.NewRunner(ctx, opts.DryRun, opts.Out)
	if opts.LogOut != nil {
		r.RawOut = opts.LogOut
	}
	channel, err := t.Updater.ResolveChannel(opts.Channel)
	if err != nil {
		return logUpgradeError(ctx, "upgrade.resolve_channel_failed", fmt.Errorf("resolve update channel: %w", err))
	}

	bundleState, err := resolveBundleVersion(ctx, t)
	if err != nil {
		return err
	}
	noteBundleVersion(r, t, channel, bundleState)

	m, err := fetchManifest(ctx, t, bundleState.CurrentVersion, channel)
	if err != nil {
		if errors.Is(err, errNoUpdate) {
			notef(r, noUpdateMessage(t.ID, channel))
			return nil
		}
		return err
	}
	notef(r, fmt.Sprintf("target=%s manifest: name=%s url=%s", t.ID, m.Name, m.URL))
	if m.URL == "" || m.Name == "" {
		return logUpgradeError(ctx, "upgrade.manifest_missing_fields", fmt.Errorf("manifest is missing url or name field: %+v", m))
	}
	if !bundleState.Missing && m.Name == bundleState.CurrentVersion {
		if err := handleCurrentVersion(ctx, r, t, bundleState.CurrentVersion, opts); err != nil {
			return err
		}
		return nil
	}

	originalDR := ""
	if !bundleState.Missing {
		originalDR, err = loadOriginalDR(ctx, t)
		if err != nil {
			return err
		}
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
	if bundleState.Missing {
		originalDR, err = captureOriginalDRFromExtractedApp(ctx, r, t, extractedApp, opts.DryRun)
		if err != nil {
			return err
		}
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
	if err := patchBundleAfterUpgrade(ctx, r, t, opts); err != nil {
		return err
	}

	notef(r, fmt.Sprintf("target=%s upgrade to %s complete", t.ID, m.Name))
	return nil
}

func patchBundleAfterUpgrade(ctx context.Context, r *patch.Runner, t targets.Target, opts Options) error {
	patchOpts := patch.Options{
		DryRun:          opts.DryRun,
		MigrateKeychain: opts.MigrateKeychain,
		Out:             opts.Out,
		LogOut:          r.RawOut,
		Trace:           nil,
	}
	if err := patch.Patch(ctx, t, patchOpts); err != nil {
		return logUpgradeError(ctx, "upgrade.repatch_failed", fmt.Errorf("re-patch after swap: %w", err))
	}
	return nil
}

func resolveBundleVersion(ctx context.Context, t targets.Target) (bundleVersionState, error) {
	if _, err := os.Stat(t.AppPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return bundleVersionState{CurrentVersion: missingBundleCurrentVersion, Missing: true}, nil
		}
		return bundleVersionState{}, logUpgradeError(ctx, "upgrade.bundle_stat_failed", fmt.Errorf("stat bundle at %s: %w", t.AppPath, err))
	}
	currentVersion, err := readBundleVersion(ctx, t)
	if err != nil {
		return bundleVersionState{}, err
	}
	return bundleVersionState{CurrentVersion: currentVersion, Missing: false}, nil
}

func noteBundleVersion(r *patch.Runner, t targets.Target, channel string, state bundleVersionState) {
	if channel == "" {
		notef(r, fmt.Sprintf("target=%s current version=%s updater=%s", t.ID, state.CurrentVersion, t.Updater.Kind))
	} else {
		notef(r, fmt.Sprintf("target=%s current version=%s channel=%s updater=%s", t.ID, state.CurrentVersion, channel, t.Updater.Kind))
	}
	if state.Missing {
		notef(r, fmt.Sprintf("target=%s app missing at %s; installing latest bundle", t.ID, t.AppPath))
	}
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
	case targets.UpdaterHTTPPathJSONManifest:
		return fetchHTTPPathJSONManifest(ctx, t, currentVersion, channel)
	case targets.UpdaterSparkleAppcast:
		return fetchSparkleManifest(ctx, t, channel)
	case targets.UpdaterSquirrelJSON:
		return fetchSquirrelJSONManifest(ctx, t)
	default:
		return updateManifest{}, fmt.Errorf("target %s has unsupported updater kind %q", t.ID, t.Updater.Kind)
	}
}

func fetchHTTPPathJSONManifest(ctx context.Context, t targets.Target, currentVersion, channel string) (updateManifest, error) {
	endpoint := renderUpdaterTemplate(t.Updater.URLTemplate, map[string]string{
		"platform": t.Updater.Platform,
		"product":  t.Updater.Product,
		"version":  currentVersion,
		"commit":   placeholderCommit(),
		"channel":  channel,
	})
	body, err := fetchURL(ctx, endpoint, renderUserAgent(t.Updater.UserAgent, currentVersion), "application/json", 1<<16)
	if err != nil {
		if errors.Is(err, errNoUpdate) {
			return updateManifest{}, errNoUpdate
		}
		return updateManifest{}, err
	}
	return parseHTTPPathJSONManifest(body)
}

func fetchSparkleManifest(ctx context.Context, t targets.Target, channel string) (updateManifest, error) {
	endpoint, err := t.Updater.URLWithChannel(channel)
	if err != nil {
		return updateManifest{}, logUpgradeError(ctx, "upgrade.sparkle_updater_url_failed", fmt.Errorf("resolve Sparkle updater URL: %w", err))
	}
	body, err := fetchURL(ctx, endpoint, renderUserAgent(t.Updater.UserAgent, ""), "application/xml", 2<<20)
	if err != nil {
		return updateManifest{}, err
	}
	return parseSparkleAppcast(body)
}

func fetchSquirrelJSONManifest(ctx context.Context, t targets.Target) (updateManifest, error) {
	endpoint, err := url.Parse(t.Updater.URL)
	if err != nil {
		return updateManifest{}, logUpgradeError(ctx, "upgrade.squirrel_updater_url_parse_failed", fmt.Errorf("parse squirrel updater URL: %w", err))
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
	body, err := fetchURL(ctx, endpoint.String(), renderUserAgent(t.Updater.UserAgent, ""), "application/json", 1<<16)
	if err != nil {
		return updateManifest{}, err
	}
	return parseSquirrelJSONManifest(body)
}

var errNoUpdate = errors.New("no update available")

func noUpdateMessage(targetID string, channel string) string {
	if channel == "" {
		return fmt.Sprintf("target=%s no update available; nothing to do", targetID)
	}
	return fmt.Sprintf("target=%s no update available on %s channel; nothing to do", targetID, channel)
}

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

func parseHTTPPathJSONManifest(body []byte) (updateManifest, error) {
	m := pathJSONManifest{URL: "", Name: ""}
	if err := json.Unmarshal(body, &m); err != nil {
		return updateManifest{}, logUpgradeErrorNoContext("upgrade.http_path_json_manifest_parse_failed", fmt.Errorf("parse path JSON manifest: %w (body=%s)", err, string(body)))
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

func parseSquirrelJSONManifest(body []byte) (updateManifest, error) {
	m := squirrelJSONManifest{CurrentRelease: "", Releases: nil}
	if err := json.Unmarshal(body, &m); err != nil {
		return updateManifest{}, logUpgradeErrorNoContext("upgrade.squirrel_manifest_parse_failed", fmt.Errorf("parse Squirrel manifest JSON: %w (body=%s)", err, string(body)))
	}
	for _, release := range m.Releases {
		if release.UpdateTo.URL == "" {
			continue
		}
		name := firstNonEmpty(release.UpdateTo.Version, release.Version, release.UpdateTo.Name)
		return updateManifest{URL: release.UpdateTo.URL, Name: name, Signature: ""}, nil
	}
	return updateManifest{}, errors.New("squirrel manifest contains no updateTo.url")
}

func renderUpdaterTemplate(template string, values map[string]string) string {
	rendered := template
	for key, value := range values {
		rendered = strings.ReplaceAll(rendered, "{"+key+"}", url.PathEscape(value))
	}
	return rendered
}

func renderUserAgent(template string, version string) string {
	return strings.ReplaceAll(template, "{version}", version)
}

func placeholderCommit() string {
	return strings.Repeat("0", 64)
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

// loadOriginalDR reads the recorded upstream DR string from state.json and
// falls back to the configured bootstrap strategy when state is missing.
func loadOriginalDR(ctx context.Context, t targets.Target) (string, error) {
	dr, err := patch.OriginalDesignatedRequirement(ctx, t)
	if err == nil {
		return verifyOriginalRequirementIsUpstream(t, dr)
	}
	if !errors.Is(err, patch.ErrMissingStateEntry) {
		return "", logUpgradeError(ctx, "upgrade.original_dr_load_failed", fmt.Errorf("load original designated requirement: %w", err))
	}
	capability := t.BootstrapCapability()
	if capability == "" {
		return "", logUpgradeError(ctx, "upgrade.original_dr_load_failed", fmt.Errorf("load original designated requirement: %w", err))
	}
	strategy, ok := lookupBootstrapStrategy(capability)
	if !ok {
		return "", logUpgradeError(ctx, "upgrade.original_dr_bootstrap_capability_unknown", fmt.Errorf("unknown original DR bootstrap capability %q", capability))
	}
	return strategy(ctx, t)
}

func bootstrapOriginalDRFromCleanMainBinary(ctx context.Context, t targets.Target) (string, error) {
	realPath := paths.RealBinaryPath(t)
	if _, err := os.Stat(realPath); err == nil {
		return "", logUpgradeError(ctx, "upgrade.original_dr_state_missing_real_exists", fmt.Errorf("target=%s has no state entry but %s exists; reinstall a clean vendor app and patch again before upgrade", t.ID, realPath))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", logUpgradeError(ctx, "upgrade.original_dr_real_stat_failed", fmt.Errorf("stat %s: %w", realPath, err))
	}
	dr, err := readDesignatedRequirement(ctx, paths.MainBinaryPath(t))
	if err != nil {
		return "", logUpgradeError(ctx, "upgrade.original_dr_capture_failed", fmt.Errorf("capture designated requirement from clean app: %w", err))
	}
	return verifyOriginalRequirementIsUpstream(t, dr)
}

func verifyOriginalRequirementIsUpstream(t targets.Target, dr string) (string, error) {
	if strings.Contains(dr, paths.SignTeamID()) {
		return "", logUpgradeErrorNoContext("upgrade.original_dr_identifies_local_team", fmt.Errorf("target=%s DesignatedRequirement identifies local signing team %s, not upstream", t.ID, paths.SignTeamID()))
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
			DryRun:          opts.DryRun,
			MigrateKeychain: opts.MigrateKeychain,
			Out:             opts.Out,
			LogOut:          r.RawOut,
			Trace:           nil,
		}); err != nil {
			return logUpgradeError(ctx, "upgrade.current_version_patch_failed", fmt.Errorf("patch clean bundle after version check: %w", err))
		}
		return nil
	}
	if _, err := loadOriginalDR(ctx, t); err != nil {
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
	if _, err := os.Stat(expected); err == nil {
		return expected, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", logUpgradeError(ctx, "upgrade.extracted_app_stat_failed", fmt.Errorf("stat expected app %s: %w", expected, err))
	}
	fallbackApp, err := singleExtractedAppRoot(extractDir)
	if err != nil {
		return "", logUpgradeError(ctx, "upgrade.extracted_app_stat_failed", fmt.Errorf("expected %s inside zip: %w", filepath.Base(t.AppPath), err))
	}
	notef(r, fmt.Sprintf("target=%s extracted app root %s differs from configured app name %s", t.ID, filepath.Base(fallbackApp), filepath.Base(t.AppPath)))
	return fallbackApp, nil
}

func singleExtractedAppRoot(extractDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(extractDir, "*.app"))
	if err != nil {
		upgradeLog.Error("upgrade.extracted_app_glob_failed", "extract_dir", extractDir, "err", err)
		return "", fmt.Errorf("glob extracted apps under %s: %w", extractDir, err)
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no .app bundle found under %s", extractDir)
	}
	return "", fmt.Errorf("multiple .app bundles found under %s: %s", extractDir, strings.Join(matches, ","))
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

func captureOriginalDRFromExtractedApp(
	ctx context.Context,
	r *patch.Runner,
	t targets.Target,
	extractedApp string,
	dryRun bool,
) (string, error) {
	executablePath := filepath.Join(extractedApp, "Contents", "MacOS", t.ExecName)
	notef(r, fmt.Sprintf("target=%s capture upstream DR from downloaded executable %s", t.ID, executablePath))
	if dryRun {
		return "", nil
	}
	dr, err := readDesignatedRequirement(ctx, executablePath)
	if err != nil {
		return "", logUpgradeError(ctx, "upgrade.missing_bundle_original_dr_capture_failed", fmt.Errorf("capture designated requirement from downloaded app: %w", err))
	}
	return verifyOriginalRequirementIsUpstream(t, dr)
}

// swapBundle removes the existing /Applications/<App>.app and installs the
// freshly extracted upstream copy.
func swapBundle(ctx context.Context, r *patch.Runner, t targets.Target, extractedApp string, dryRun bool) error {
	upgradeLog.DebugContext(ctx, "upgrade.swap_bundle.boundary", "target", t.ID, "app_path", t.AppPath, "extracted_app", extractedApp, "dry_run", dryRun)
	notef(r, "removing patched bundle "+t.AppPath)
	if !dryRun {
		if err := os.RemoveAll(t.AppPath); err != nil {
			return logUpgradeError(ctx, "upgrade.remove_current_bundle_failed", fmt.Errorf("remove %s: %w", t.AppPath, err))
		}
	}
	notef(r, fmt.Sprintf("installing fresh bundle %s -> %s", extractedApp, t.AppPath))
	if err := r.Run(ctx, "/usr/bin/ditto", extractedApp, t.AppPath); err != nil {
		return logUpgradeError(ctx, "upgrade.install_fresh_bundle_failed", fmt.Errorf("install fresh bundle %s -> %s: %w", extractedApp, t.AppPath, err))
	}
	return nil
}
