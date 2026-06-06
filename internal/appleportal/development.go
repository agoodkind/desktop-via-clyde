package appleportal

// This file adds the App Store Connect automation that mints the one-time Apple
// Development signing assets the devsign overlay needs: an Apple Development
// certificate from a locally generated CSR, the current Mac registered as a
// development device, and a wildcard MAC_APP_DEVELOPMENT provisioning profile that
// authorizes the team-scoped keychain-access-groups. Each ensure-step finds an
// existing resource before creating one, mirroring EnsureBundleID, so re-running
// the flow does not churn the developer account. Secrets (the generated private
// key, the p12, its password) are written to files with 0600 permissions and are
// never logged.

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"goodkind.io/desktop-via-clyde/internal/config"
)

const (
	developmentCertType    = "DEVELOPMENT"
	developmentProfileType = "MAC_APP_DEVELOPMENT"
	wildcardBundleID       = "*"
	devSigningDirName      = "dev-signing"
	defaultAssetBaseName   = "development"
	opensslPath            = "/usr/bin/openssl"
	systemProfilerPath     = "/usr/sbin/system_profiler"
	rsaKeyBits             = 2048
	p12PasswordBytes       = 24
	devSigningDirPerm      = 0o700
	secretFilePerm         = 0o600
	provisioningUDIDLabel  = "Provisioning UDID"
	hardwareUUIDLabel      = "Hardware UUID"
)

// commandRunner runs an external command. It is injected so the openssl and
// system_profiler shell-outs can be faked in tests without touching the system.
type commandRunner func(ctx context.Context, name string, args ...string) error

func execRun(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		runErr := fmt.Errorf("%s: %w (output: %s)", name, err, strings.TrimSpace(string(output)))
		appleportalLog.ErrorContext(ctx, "appleportal.exec_command_failed", "command", name, "err", runErr)
		return runErr
	}
	return nil
}

// Device is a registered development device resource.
type Device struct {
	ID   string
	Name string
	UDID string
}

type certificateCreateAttributes struct {
	CertificateType string `json:"certificateType"`
	CsrContent      string `json:"csrContent"`
}

type certificateCreateData struct {
	Type       string                      `json:"type"`
	Attributes certificateCreateAttributes `json:"attributes"`
}

type certificateCreateRequest struct {
	Data certificateCreateData `json:"data"`
}

type certificateContentAttributes struct {
	Name               string `json:"name"`
	SerialNumber       string `json:"serialNumber"`
	CertificateType    string `json:"certificateType"`
	CertificateContent string `json:"certificateContent"`
}

type certificateContentRecord struct {
	ID         string                       `json:"id"`
	Type       string                       `json:"type"`
	Attributes certificateContentAttributes `json:"attributes"`
}

type certificateContentEnvelope struct {
	Data certificateContentRecord `json:"data"`
}

type deviceAttributes struct {
	Name     string `json:"name"`
	UDID     string `json:"udid"`
	Platform string `json:"platform"`
}

type deviceRecord struct {
	ID         string           `json:"id"`
	Type       string           `json:"type"`
	Attributes deviceAttributes `json:"attributes"`
}

type deviceListEnvelope struct {
	Data []deviceRecord `json:"data"`
}

type deviceEnvelope struct {
	Data deviceRecord `json:"data"`
}

type deviceCreateAttributes struct {
	Name     string `json:"name"`
	Platform string `json:"platform"`
	UDID     string `json:"udid"`
}

type deviceCreateData struct {
	Type       string                 `json:"type"`
	Attributes deviceCreateAttributes `json:"attributes"`
}

type deviceCreateRequest struct {
	Data deviceCreateData `json:"data"`
}

type profileDevCreateRelationships struct {
	BundleID     relationshipOne  `json:"bundleId"`
	Certificates relationshipMany `json:"certificates"`
	Devices      relationshipMany `json:"devices"`
}

type profileDevCreateData struct {
	Type          string                        `json:"type"`
	Attributes    profileCreateAttributes       `json:"attributes"`
	Relationships profileDevCreateRelationships `json:"relationships"`
}

type profileDevCreateRequest struct {
	Data profileDevCreateData `json:"data"`
}

type profileListEnvelope struct {
	Data []profileRecord `json:"data"`
}

// DevSigningDir returns the directory that holds the generated development-signing
// assets, ~/.local/state/clyde/dev-signing by default (XDG_STATE_HOME aware).
func DevSigningDir() string {
	return filepath.Join(config.StateRoot(), devSigningDirName)
}

// GenerateDevelopmentCSR creates an RSA 2048 key pair and a PKCS#10 certificate
// signing request for an Apple Development certificate. It returns the CSR as a
// PEM string (the shape POST /v1/certificates expects in csrContent) and the
// private key as PEM bytes; the caller must persist the key with restrictive
// permissions because it is the secret half of the signing identity.
func GenerateDevelopmentCSR(commonName, email string) (string, []byte, error) {
	key, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	if err != nil {
		genErr := fmt.Errorf("generate RSA key: %w", err)
		appleportalLog.Error("appleportal.generate_rsa_key_failed", "err", genErr)
		return "", nil, genErr
	}
	template := x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: commonName},
		SignatureAlgorithm: x509.SHA256WithRSA,
	}
	if strings.TrimSpace(email) != "" {
		template.EmailAddresses = []string{email}
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &template, key)
	if err != nil {
		csrErr := fmt.Errorf("create certificate request: %w", err)
		appleportalLog.Error("appleportal.create_csr_failed", "err", csrErr)
		return "", nil, csrErr
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		keyErr := fmt.Errorf("marshal private key: %w", err)
		appleportalLog.Error("appleportal.marshal_private_key_failed", "err", keyErr)
		return "", nil, keyErr
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return string(csrPEM), keyPEM, nil
}

// FindDevelopmentCertificate returns the Apple Development certificate whose
// serial number matches serialNumber, or nil when none exists. It is the
// find-existing half of the idempotent certificate flow: the orchestrator records
// the serial of the certificate it created so a later run can reuse it instead of
// minting another one.
func (c *Client) FindDevelopmentCertificate(ctx context.Context, serialNumber string) (*Certificate, error) {
	query := url.Values{}
	query.Set("filter[certificateType]", developmentCertType)
	query.Set("fields[certificates]", "name,serialNumber,certificateType")
	query.Set("limit", "200")
	responseBody, err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/certificates", query, nil)
	if err != nil {
		return nil, err
	}
	var envelope certificateListEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_certificates_failed", "err", err)
		return nil, fmt.Errorf("decode certificates response: %w", err)
	}
	for _, record := range envelope.Data {
		if record.Attributes.CertificateType != developmentCertType {
			continue
		}
		if strings.EqualFold(record.Attributes.SerialNumber, serialNumber) {
			return &Certificate{
				ID:           record.ID,
				Name:         record.Attributes.Name,
				SerialNumber: record.Attributes.SerialNumber,
			}, nil
		}
	}
	return nil, nil
}

// CreateDevelopmentCertificate submits csrPEM to App Store Connect and returns the
// minted Apple Development certificate together with its DER-encoded bytes (decoded
// from the base64 certificateContent), which the caller pairs with the CSR's
// private key to build the leaf-only p12 rcodesign signs with.
func (c *Client) CreateDevelopmentCertificate(ctx context.Context, csrPEM string) (Certificate, []byte, error) {
	requestBody, err := json.Marshal(certificateCreateRequest{Data: certificateCreateData{
		Type: "certificates",
		Attributes: certificateCreateAttributes{
			CertificateType: developmentCertType,
			CsrContent:      csrPEM,
		},
	}})
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.encode_certificate_failed", "err", err)
		return Certificate{}, nil, fmt.Errorf("encode certificate request: %w", err)
	}
	responseBody, err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/certificates", nil, requestBody)
	if err != nil {
		return Certificate{}, nil, err
	}
	var envelope certificateContentEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_certificate_failed", "err", err)
		return Certificate{}, nil, fmt.Errorf("decode create certificate response: %w", err)
	}
	certificate := Certificate{
		ID:           envelope.Data.ID,
		Name:         envelope.Data.Attributes.Name,
		SerialNumber: envelope.Data.Attributes.SerialNumber,
	}
	der, err := base64.StdEncoding.DecodeString(envelope.Data.Attributes.CertificateContent)
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_certificate_content_failed", "err", err)
		return Certificate{}, nil, fmt.Errorf("decode certificate content: %w", err)
	}
	return certificate, der, nil
}

// EnsureDevelopmentDevice returns the development device resource for udid,
// registering this Mac under the API key's team when it is not already present.
func (c *Client) EnsureDevelopmentDevice(ctx context.Context, name, udid string) (Device, error) {
	existing, err := c.findDeviceByUDID(ctx, udid)
	if err != nil {
		return Device{}, err
	}
	if existing != nil {
		return Device{ID: existing.ID, Name: existing.Attributes.Name, UDID: existing.Attributes.UDID}, nil
	}
	requestBody, err := json.Marshal(deviceCreateRequest{Data: deviceCreateData{
		Type: "devices",
		Attributes: deviceCreateAttributes{
			Name:     name,
			Platform: macOSPlatform,
			UDID:     udid,
		},
	}})
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.encode_device_failed", "err", err)
		return Device{}, fmt.Errorf("encode device request: %w", err)
	}
	responseBody, err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/devices", nil, requestBody)
	if err != nil {
		return Device{}, err
	}
	var envelope deviceEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_device_failed", "err", err)
		return Device{}, fmt.Errorf("decode create device response: %w", err)
	}
	return Device{ID: envelope.Data.ID, Name: envelope.Data.Attributes.Name, UDID: envelope.Data.Attributes.UDID}, nil
}

func (c *Client) findDeviceByUDID(ctx context.Context, udid string) (*deviceRecord, error) {
	query := url.Values{}
	query.Set("filter[udid]", udid)
	query.Set("fields[devices]", "name,udid,platform")
	responseBody, err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/devices", query, nil)
	if err != nil {
		return nil, err
	}
	var envelope deviceListEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_devices_failed", "err", err)
		return nil, fmt.Errorf("decode devices response: %w", err)
	}
	if len(envelope.Data) == 0 {
		return nil, nil
	}
	return &envelope.Data[0], nil
}

// EnsureDevelopmentProfile returns the wildcard MAC_APP_DEVELOPMENT provisioning
// profile named name, creating it from the bundle id, certificate, and device
// resources when it does not already exist. The created profile authorizes the
// team-scoped keychain-access-groups that the device-key SecItemAdd needs at
// runtime.
func (c *Client) EnsureDevelopmentProfile(ctx context.Context, name, bundleIDResourceID, certificateResourceID, deviceResourceID string) (Profile, error) {
	existing, err := c.findProfileByName(ctx, name)
	if err != nil {
		return Profile{}, err
	}
	if existing != nil {
		return Profile{ID: existing.ID, Name: existing.Attributes.Name}, nil
	}
	requestBody, err := json.Marshal(profileDevCreateRequest{Data: profileDevCreateData{
		Type: "profiles",
		Attributes: profileCreateAttributes{
			Name:        name,
			ProfileType: developmentProfileType,
		},
		Relationships: profileDevCreateRelationships{
			BundleID:     relationshipOne{Data: resourceRef{Type: "bundleIds", ID: bundleIDResourceID}},
			Certificates: relationshipMany{Data: []resourceRef{{Type: "certificates", ID: certificateResourceID}}},
			Devices:      relationshipMany{Data: []resourceRef{{Type: "devices", ID: deviceResourceID}}},
		},
	}})
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.encode_dev_profile_failed", "err", err)
		return Profile{}, fmt.Errorf("encode development profile request: %w", err)
	}
	responseBody, err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/profiles", nil, requestBody)
	if err != nil {
		return Profile{}, err
	}
	var envelope profileEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_dev_profile_failed", "err", err)
		return Profile{}, fmt.Errorf("decode create development profile response: %w", err)
	}
	return Profile{ID: envelope.Data.ID, Name: envelope.Data.Attributes.Name}, nil
}

func (c *Client) findProfileByName(ctx context.Context, name string) (*profileRecord, error) {
	query := url.Values{}
	query.Set("filter[name]", name)
	query.Set("fields[profiles]", "name,profileType")
	responseBody, err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/profiles", query, nil)
	if err != nil {
		return nil, err
	}
	var envelope profileListEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_profiles_failed", "err", err)
		return nil, fmt.Errorf("decode profiles response: %w", err)
	}
	for i := range envelope.Data {
		if envelope.Data[i].Attributes.Name == name {
			return &envelope.Data[i], nil
		}
	}
	return nil, nil
}

// parseProvisioningUDID extracts the development device identifier from
// system_profiler SPHardwareDataType output. Apple Silicon Macs expose a
// "Provisioning UDID" line, which the developer portal registers; Intel Macs fall
// back to the "Hardware UUID".
func parseProvisioningUDID(output string) (string, error) {
	provisioning := ""
	hardware := ""
	for line := range strings.SplitSeq(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if value, ok := strings.CutPrefix(trimmed, provisioningUDIDLabel+":"); ok {
			provisioning = strings.TrimSpace(value)
			continue
		}
		if value, ok := strings.CutPrefix(trimmed, hardwareUUIDLabel+":"); ok {
			hardware = strings.TrimSpace(value)
		}
	}
	if provisioning != "" {
		return provisioning, nil
	}
	if hardware != "" {
		return hardware, nil
	}
	return "", fmt.Errorf("no %q or %q found in system_profiler output", provisioningUDIDLabel, hardwareUUIDLabel)
}

// ProvisioningUDID returns this Mac's development provisioning identifier by
// reading system_profiler SPHardwareDataType.
func ProvisioningUDID(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, systemProfilerPath, "SPHardwareDataType")
	output, err := cmd.Output()
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.system_profiler_failed", "err", err)
		return "", fmt.Errorf("read hardware data: %w", err)
	}
	return parseProvisioningUDID(string(output))
}

// DevelopmentAssetOptions controls how GenerateDevelopmentAssets mints the
// development-signing bundle. Zero values fall back to defaults: a wildcard bundle
// id, the XDG dev-signing directory, the host name as the device name, and this
// Mac's discovered provisioning UDID.
type DevelopmentAssetOptions struct {
	BundleID    string
	ProfileName string
	DeviceName  string
	DeviceUDID  string
	DestDir     string
	BaseName    string

	// client and run are injection seams for tests. Production callers leave them
	// nil; GenerateDevelopmentAssets then builds a real client from the discovered
	// credentials and shells out to openssl and system_profiler.
	client *Client
	run    commandRunner
}

// DevelopmentAssetResult reports the files GenerateDevelopmentAssets wrote and the
// App Store Connect resources it used.
type DevelopmentAssetResult struct {
	ProfilePath     string
	P12Path         string
	P12PasswordFile string
	CertificateID   string
	DeviceID        string
	ProfileID       string
	ReusedCert      bool
}

// GenerateDevelopmentAssets mints (or refreshes) the development-signing assets and
// writes them into the dev-signing directory. Every App Store Connect step is
// idempotent: the device is matched by UDID, the bundle id by identifier, the
// profile by name, and the certificate by a serial recorded from a prior run, so a
// repeated call reuses existing resources instead of creating new ones. It is the
// one place that contacts Apple; the patch flow only consumes the files it writes.
func GenerateDevelopmentAssets(ctx context.Context, opts DevelopmentAssetOptions) (DevelopmentAssetResult, error) {
	if strings.TrimSpace(opts.BundleID) == "" {
		opts.BundleID = wildcardBundleID
	}
	if strings.TrimSpace(opts.BaseName) == "" {
		opts.BaseName = defaultAssetBaseName
	}
	if strings.TrimSpace(opts.DestDir) == "" {
		opts.DestDir = DevSigningDir()
	}
	if strings.TrimSpace(opts.DeviceName) == "" {
		host, err := os.Hostname()
		if err != nil || strings.TrimSpace(host) == "" {
			host = "desktop-via-clyde mac"
		}
		opts.DeviceName = host
	}
	if strings.TrimSpace(opts.ProfileName) == "" {
		opts.ProfileName = sanitizeAppleName("desktop-via-clyde development " + opts.BundleID)
	}
	if opts.run == nil {
		opts.run = execRun
	}
	if opts.client == nil {
		credentials, err := DiscoverCredentials()
		if err != nil {
			return DevelopmentAssetResult{}, err
		}
		client, err := NewClient(credentials)
		if err != nil {
			return DevelopmentAssetResult{}, err
		}
		opts.client = client
	}
	if strings.TrimSpace(opts.DeviceUDID) == "" {
		udid, err := ProvisioningUDID(ctx)
		if err != nil {
			return DevelopmentAssetResult{}, err
		}
		opts.DeviceUDID = udid
	}

	if err := os.MkdirAll(opts.DestDir, devSigningDirPerm); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.dev_signing_mkdir_failed", "dir", opts.DestDir, "err", err)
		return DevelopmentAssetResult{}, fmt.Errorf("create dev-signing dir %s: %w", opts.DestDir, err)
	}

	device, err := opts.client.EnsureDevelopmentDevice(ctx, opts.DeviceName, opts.DeviceUDID)
	if err != nil {
		return DevelopmentAssetResult{}, err
	}
	bundleID, err := opts.client.EnsureBundleID(ctx, opts.BundleID, sanitizeAppleName("desktop-via-clyde "+opts.BundleID))
	if err != nil {
		return DevelopmentAssetResult{}, err
	}

	p12Path := filepath.Join(opts.DestDir, opts.BaseName+".p12")
	passwordPath := filepath.Join(opts.DestDir, opts.BaseName+"-p12-password.txt")
	serialPath := filepath.Join(opts.DestDir, opts.BaseName+"-cert-serial.txt")
	profilePath := filepath.Join(opts.DestDir, opts.BaseName+".provisionprofile")

	certificateID, reused, err := opts.ensureCertificate(ctx, p12Path, passwordPath, serialPath)
	if err != nil {
		return DevelopmentAssetResult{}, err
	}

	profile, err := opts.client.EnsureDevelopmentProfile(ctx, opts.ProfileName, bundleID.ID, certificateID, device.ID)
	if err != nil {
		return DevelopmentAssetResult{}, err
	}
	if err := opts.client.DownloadProfile(ctx, profile.ID, profilePath); err != nil {
		return DevelopmentAssetResult{}, err
	}

	appleportalLog.InfoContext(ctx, "appleportal.development_assets_generated",
		"profile", profile.Name, "device", device.UDID, "reused_cert", reused, "dest", opts.DestDir)
	return DevelopmentAssetResult{
		ProfilePath:     profilePath,
		P12Path:         p12Path,
		P12PasswordFile: passwordPath,
		CertificateID:   certificateID,
		DeviceID:        device.ID,
		ProfileID:       profile.ID,
		ReusedCert:      reused,
	}, nil
}

// ensureCertificate reuses the certificate recorded by a prior run when its serial
// still resolves in App Store Connect and the p12 is present on disk; otherwise it
// mints a fresh Apple Development certificate, writes the leaf-only p12, and records
// the serial for the next run.
func (opts DevelopmentAssetOptions) ensureCertificate(ctx context.Context, p12Path, passwordPath, serialPath string) (string, bool, error) {
	if recordedSerial := readTrimmedFile(serialPath); recordedSerial != "" && fileExists(p12Path) && fileExists(passwordPath) {
		existing, err := opts.client.FindDevelopmentCertificate(ctx, recordedSerial)
		if err != nil {
			return "", false, err
		}
		if existing != nil {
			return existing.ID, true, nil
		}
	}

	commonName := "desktop-via-clyde development " + opts.BundleID
	csrPEM, keyPEM, err := GenerateDevelopmentCSR(commonName, "")
	if err != nil {
		return "", false, err
	}
	certificate, certDER, err := opts.client.CreateDevelopmentCertificate(ctx, csrPEM)
	if err != nil {
		return "", false, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := writeLeafP12(ctx, opts.run, certPEM, keyPEM, p12Path, passwordPath); err != nil {
		return "", false, err
	}
	if err := os.WriteFile(serialPath, []byte(certificate.SerialNumber+"\n"), secretFilePerm); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.write_cert_serial_failed", "path", serialPath, "err", err)
		return "", false, fmt.Errorf("write certificate serial %s: %w", serialPath, err)
	}
	return certificate.ID, false, nil
}

// writeLeafP12 builds a leaf-only PKCS#12 from the certificate and its private key
// by shelling out to openssl. rcodesign signs from this p12 directly, so it must
// hold only the leaf (a chained p12 makes rcodesign misdetect the team). The
// random password is written to passwordPath and handed to openssl via file: so it
// never appears on the argv line.
func writeLeafP12(ctx context.Context, run commandRunner, certPEM, keyPEM []byte, p12Path, passwordPath string) error {
	password, err := randomPassword()
	if err != nil {
		return err
	}
	if err := os.WriteFile(passwordPath, []byte(password+"\n"), secretFilePerm); err != nil {
		writeErr := fmt.Errorf("write p12 password file %s: %w", passwordPath, err)
		appleportalLog.ErrorContext(ctx, "appleportal.write_p12_password_failed", "path", passwordPath, "err", writeErr)
		return writeErr
	}
	tempDir, err := os.MkdirTemp("", "dvc-leaf-p12-")
	if err != nil {
		tempErr := fmt.Errorf("create temp dir for p12 inputs: %w", err)
		appleportalLog.ErrorContext(ctx, "appleportal.p12_tempdir_failed", "err", tempErr)
		return tempErr
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	certFile := filepath.Join(tempDir, "leaf.crt")
	keyFile := filepath.Join(tempDir, "leaf.key")
	if err := os.WriteFile(certFile, certPEM, secretFilePerm); err != nil {
		certErr := fmt.Errorf("write temp certificate: %w", err)
		appleportalLog.ErrorContext(ctx, "appleportal.write_temp_cert_failed", "err", certErr)
		return certErr
	}
	if err := os.WriteFile(keyFile, keyPEM, secretFilePerm); err != nil {
		keyErr := fmt.Errorf("write temp private key: %w", err)
		appleportalLog.ErrorContext(ctx, "appleportal.write_temp_key_failed", "err", keyErr)
		return keyErr
	}
	if err := run(ctx, opensslPath,
		"pkcs12", "-export",
		"-inkey", keyFile,
		"-in", certFile,
		"-out", p12Path,
		"-name", "desktop-via-clyde development",
		"-passout", "file:"+passwordPath,
	); err != nil {
		exportErr := fmt.Errorf("export leaf p12: %w", err)
		appleportalLog.ErrorContext(ctx, "appleportal.export_p12_failed", "path", p12Path, "err", exportErr)
		return exportErr
	}
	if err := os.Chmod(p12Path, secretFilePerm); err != nil {
		chmodErr := fmt.Errorf("chmod p12 %s: %w", p12Path, err)
		appleportalLog.ErrorContext(ctx, "appleportal.chmod_p12_failed", "path", p12Path, "err", chmodErr)
		return chmodErr
	}
	return nil
}

func randomPassword() (string, error) {
	raw := make([]byte, p12PasswordBytes)
	if _, err := rand.Read(raw); err != nil {
		randErr := fmt.Errorf("read random password bytes: %w", err)
		appleportalLog.Error("appleportal.random_password_failed", "err", randErr)
		return "", randErr
	}
	return hex.EncodeToString(raw), nil
}

func readTrimmedFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
