// Package appleportal generates Developer ID provisioning profiles through the
// App Store Connect API. A locally re-signed app needs a provisioning profile
// bound to the local signing team so it can claim team-scoped entitlements such
// as keychain access groups.
package appleportal

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"goodkind.io/desktop-via-clyde/internal/clock"
)

var appleportalLog = slog.With("component", "desktop-via-clyde", "subcomponent", "appleportal")

const (
	baseURL           = "https://api.appstoreconnect.apple.com/v1"
	devCertsDirName   = "dev-certs"
	authKeyPrefix     = "AuthKey_"
	authKeySuffix     = ".p8"
	readmeName        = "README.md"
	directProfileType = "MAC_APP_DIRECT"
	developerIDType   = "DEVELOPER_ID_APPLICATION"
	macOSPlatform     = "MAC_OS"
	tokenAudience     = "appstoreconnect-v1"
	tokenLifetime     = 20 * time.Minute
	httpTimeout       = 30 * time.Second
)

var issuerIDPattern = regexp.MustCompile("issuer ID `([0-9a-fA-F-]+)`")

// Credentials identifies an App Store Connect API key and its issuer.
type Credentials struct {
	KeyID    string
	IssuerID string
	KeyPath  string
}

// Client calls the App Store Connect API with a signed bearer token.
type Client struct {
	httpClient *http.Client
	token      string
}

// BundleID is a registered App ID resource.
type BundleID struct {
	ID         string
	Identifier string
}

// Certificate is a signing certificate resource.
type Certificate struct {
	ID           string
	Name         string
	SerialNumber string
}

// Profile is a provisioning profile resource.
type Profile struct {
	ID   string
	Name string
}

type apiErrorDetail struct {
	Status string `json:"status"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type apiErrorResponse struct {
	Errors []apiErrorDetail `json:"errors"`
}

type bundleIDAttributes struct {
	Identifier string `json:"identifier"`
	Name       string `json:"name"`
}

type bundleIDRecord struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Attributes bundleIDAttributes `json:"attributes"`
}

type certificateAttributes struct {
	Name            string `json:"name"`
	SerialNumber    string `json:"serialNumber"`
	CertificateType string `json:"certificateType"`
}

type certificateRecord struct {
	ID         string                `json:"id"`
	Type       string                `json:"type"`
	Attributes certificateAttributes `json:"attributes"`
}

type profileAttributes struct {
	Name           string `json:"name"`
	ProfileType    string `json:"profileType"`
	ProfileContent string `json:"profileContent"`
}

type profileRecord struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Attributes profileAttributes `json:"attributes"`
}

type resourceRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

type relationshipOne struct {
	Data resourceRef `json:"data"`
}

type relationshipMany struct {
	Data []resourceRef `json:"data"`
}

type bundleIDCreateAttributes struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Platform   string `json:"platform"`
}

type bundleIDCreateData struct {
	Type       string                   `json:"type"`
	Attributes bundleIDCreateAttributes `json:"attributes"`
}

type bundleIDCreateRequest struct {
	Data bundleIDCreateData `json:"data"`
}

type profileCreateAttributes struct {
	Name        string `json:"name"`
	ProfileType string `json:"profileType"`
}

type profileCreateRelationships struct {
	BundleID     relationshipOne  `json:"bundleId"`
	Certificates relationshipMany `json:"certificates"`
}

type profileCreateData struct {
	Type          string                     `json:"type"`
	Attributes    profileCreateAttributes    `json:"attributes"`
	Relationships profileCreateRelationships `json:"relationships"`
}

type profileCreateRequest struct {
	Data profileCreateData `json:"data"`
}

type bundleIDListEnvelope struct {
	Data []bundleIDRecord `json:"data"`
}

type bundleIDEnvelope struct {
	Data bundleIDRecord `json:"data"`
}

type certificateListEnvelope struct {
	Data []certificateRecord `json:"data"`
}

type profileEnvelope struct {
	Data profileRecord `json:"data"`
}

// ProvisionDeveloperIDProfile ensures a Developer ID provisioning profile bound
// to bundleIdentifier and the local signing identity exists, then writes the
// decoded profile to destinationPath. The bundle identifier is registered under
// the API key's team when it does not already exist.
func ProvisionDeveloperIDProfile(ctx context.Context, bundleIdentifier, profileName, signingIdentity, destinationPath string) error {
	credentials, err := DiscoverCredentials()
	if err != nil {
		return err
	}
	client, err := NewClient(credentials)
	if err != nil {
		return err
	}
	bundleID, err := client.EnsureBundleID(ctx, bundleIdentifier, profileName)
	if err != nil {
		return err
	}
	certificate, err := client.ResolveLocalDeveloperIDCertificate(ctx, signingIdentity)
	if err != nil {
		return err
	}
	profile, err := client.CreateDirectProfile(ctx, bundleID.ID, certificate.ID, profileName)
	if err != nil {
		return err
	}
	if err := client.DownloadProfile(ctx, profile.ID, destinationPath); err != nil {
		return err
	}
	appleportalLog.InfoContext(ctx, "appleportal.profile_provisioned",
		"bundle_id", bundleID.Identifier, "profile", profile.Name, "destination", destinationPath)
	return nil
}

// DiscoverCredentials locates the single App Store Connect API key under
// ~/Desktop/dev-certs and reads the issuer ID from that folder's README.
func DiscoverCredentials() (Credentials, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		appleportalLog.Error("appleportal.home_dir_failed", "err", err)
		return Credentials{}, fmt.Errorf("resolve home dir: %w", err)
	}
	devCertsDir := filepath.Join(homeDir, "Desktop", devCertsDirName)
	entries, err := os.ReadDir(devCertsDir)
	if err != nil {
		appleportalLog.Error("appleportal.read_dev_certs_failed", "dir", devCertsDir, "err", err)
		return Credentials{}, fmt.Errorf("read %s: %w", devCertsDir, err)
	}
	keyPaths := make([]string, 0, 1)
	keyID := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasPrefix(name, authKeyPrefix) || !strings.HasSuffix(name, authKeySuffix) {
			continue
		}
		keyPaths = append(keyPaths, filepath.Join(devCertsDir, name))
		keyID = strings.TrimSuffix(strings.TrimPrefix(name, authKeyPrefix), authKeySuffix)
	}
	if len(keyPaths) != 1 {
		countErr := fmt.Errorf("expected exactly one App Store Connect key in %s, found %d", devCertsDir, len(keyPaths))
		appleportalLog.Error("appleportal.key_count_unexpected", "dir", devCertsDir, "count", len(keyPaths), "err", countErr)
		return Credentials{}, countErr
	}
	readmePath := filepath.Join(devCertsDir, readmeName)
	readmeBytes, err := os.ReadFile(readmePath)
	if err != nil {
		appleportalLog.Error("appleportal.read_readme_failed", "path", readmePath, "err", err)
		return Credentials{}, fmt.Errorf("read %s: %w", readmePath, err)
	}
	matches := issuerIDPattern.FindStringSubmatch(string(readmeBytes))
	if len(matches) != 2 {
		issuerErr := fmt.Errorf("issuer ID not found in %s", readmePath)
		appleportalLog.Error("appleportal.issuer_id_missing", "path", readmePath, "err", issuerErr)
		return Credentials{}, issuerErr
	}
	return Credentials{KeyID: keyID, IssuerID: matches[1], KeyPath: keyPaths[0]}, nil
}

// NewClient signs an App Store Connect bearer token from credentials.
func NewClient(credentials Credentials) (*Client, error) {
	keyBytes, err := os.ReadFile(credentials.KeyPath)
	if err != nil {
		appleportalLog.Error("appleportal.read_key_failed", "path", credentials.KeyPath, "err", err)
		return nil, fmt.Errorf("read key %s: %w", credentials.KeyPath, err)
	}
	key, err := jwt.ParseECPrivateKeyFromPEM(keyBytes)
	if err != nil {
		appleportalLog.Error("appleportal.parse_key_failed", "path", credentials.KeyPath, "err", err)
		return nil, fmt.Errorf("parse EC key %s: %w", credentials.KeyPath, err)
	}
	now := clock.Now()
	claims := jwt.RegisteredClaims{
		Issuer:    credentials.IssuerID,
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(tokenLifetime)),
		Audience:  jwt.ClaimStrings{tokenAudience},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	token.Header["kid"] = credentials.KeyID
	signedToken, err := token.SignedString(key)
	if err != nil {
		appleportalLog.Error("appleportal.sign_token_failed", "err", err)
		return nil, fmt.Errorf("sign App Store Connect token: %w", err)
	}
	return &Client{httpClient: &http.Client{Timeout: httpTimeout}, token: signedToken}, nil
}

// EnsureBundleID returns the App ID resource for identifier, creating it under
// the API key's team when it does not yet exist.
func (c *Client) EnsureBundleID(ctx context.Context, identifier, name string) (BundleID, error) {
	existing, err := c.findBundleID(ctx, identifier)
	if err != nil {
		return BundleID{}, err
	}
	if existing != nil {
		return BundleID{ID: existing.ID, Identifier: existing.Attributes.Identifier}, nil
	}
	requestBody, err := json.Marshal(bundleIDCreateRequest{Data: bundleIDCreateData{
		Type: "bundleIds",
		Attributes: bundleIDCreateAttributes{
			Name:       name,
			Identifier: identifier,
			Platform:   macOSPlatform,
		},
	}})
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.encode_bundle_id_failed", "err", err)
		return BundleID{}, fmt.Errorf("encode bundle ID request: %w", err)
	}
	responseBody, err := c.doJSON(ctx, http.MethodPost, baseURL+"/bundleIds", nil, requestBody)
	if err != nil {
		return BundleID{}, err
	}
	var envelope bundleIDEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_bundle_id_failed", "err", err)
		return BundleID{}, fmt.Errorf("decode create bundle ID response: %w", err)
	}
	return BundleID{ID: envelope.Data.ID, Identifier: envelope.Data.Attributes.Identifier}, nil
}

// ResolveLocalDeveloperIDCertificate finds the App Store Connect certificate
// resource whose serial matches the local Developer ID Application identity.
func (c *Client) ResolveLocalDeveloperIDCertificate(ctx context.Context, signingIdentity string) (Certificate, error) {
	localSerial, err := localCertificateSerial(ctx, signingIdentity)
	if err != nil {
		return Certificate{}, err
	}
	query := url.Values{}
	query.Set("fields[certificates]", "name,serialNumber,certificateType")
	query.Set("limit", "200")
	responseBody, err := c.doJSON(ctx, http.MethodGet, baseURL+"/certificates", query, nil)
	if err != nil {
		return Certificate{}, err
	}
	var envelope certificateListEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_certificates_failed", "err", err)
		return Certificate{}, fmt.Errorf("decode certificates response: %w", err)
	}
	for _, certificate := range envelope.Data {
		if certificate.Attributes.CertificateType != developerIDType {
			continue
		}
		if strings.EqualFold(certificate.Attributes.SerialNumber, localSerial) {
			return Certificate{
				ID:           certificate.ID,
				Name:         certificate.Attributes.Name,
				SerialNumber: certificate.Attributes.SerialNumber,
			}, nil
		}
	}
	notFoundErr := fmt.Errorf("developer ID Application certificate with serial %s not found in App Store Connect", localSerial)
	appleportalLog.ErrorContext(ctx, "appleportal.certificate_not_found", "serial", localSerial, "err", notFoundErr)
	return Certificate{}, notFoundErr
}

// CreateDirectProfile creates a Developer ID (direct) provisioning profile that
// binds the bundle ID resource to the certificate resource.
func (c *Client) CreateDirectProfile(ctx context.Context, bundleIDResourceID, certificateResourceID, name string) (Profile, error) {
	requestBody, err := json.Marshal(profileCreateRequest{Data: profileCreateData{
		Type: "profiles",
		Attributes: profileCreateAttributes{
			Name:        name,
			ProfileType: directProfileType,
		},
		Relationships: profileCreateRelationships{
			BundleID:     relationshipOne{Data: resourceRef{Type: "bundleIds", ID: bundleIDResourceID}},
			Certificates: relationshipMany{Data: []resourceRef{{Type: "certificates", ID: certificateResourceID}}},
		},
	}})
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.encode_profile_failed", "err", err)
		return Profile{}, fmt.Errorf("encode profile request: %w", err)
	}
	responseBody, err := c.doJSON(ctx, http.MethodPost, baseURL+"/profiles", nil, requestBody)
	if err != nil {
		return Profile{}, err
	}
	var envelope profileEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_profile_failed", "err", err)
		return Profile{}, fmt.Errorf("decode create profile response: %w", err)
	}
	return Profile{ID: envelope.Data.ID, Name: envelope.Data.Attributes.Name}, nil
}

// DownloadProfile fetches the profile content and writes the decoded provisioning
// profile to destination.
func (c *Client) DownloadProfile(ctx context.Context, profileResourceID, destination string) error {
	query := url.Values{}
	query.Set("fields[profiles]", "profileContent")
	responseBody, err := c.doJSON(ctx, http.MethodGet, baseURL+"/profiles/"+profileResourceID, query, nil)
	if err != nil {
		return err
	}
	var envelope profileEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_profile_content_failed", "err", err)
		return fmt.Errorf("decode profile response: %w", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(envelope.Data.Attributes.ProfileContent)
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_profile_base64_failed", "profile", profileResourceID, "err", err)
		return fmt.Errorf("decode profile %s content: %w", profileResourceID, err)
	}
	if err := os.WriteFile(destination, decoded, 0o600); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.write_profile_failed", "destination", destination, "err", err)
		return fmt.Errorf("write profile %s: %w", destination, err)
	}
	return nil
}

func localCertificateSerial(ctx context.Context, signingIdentity string) (string, error) {
	command := exec.CommandContext(ctx, "security", "find-certificate", "-c", signingIdentity, "-p")
	pemBytes, err := command.Output()
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.find_certificate_failed", "identity", signingIdentity, "err", err)
		return "", fmt.Errorf("find local certificate for %q: %w", signingIdentity, err)
	}
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		pemErr := fmt.Errorf("decode PEM certificate for %q", signingIdentity)
		appleportalLog.ErrorContext(ctx, "appleportal.decode_certificate_pem_failed", "identity", signingIdentity, "err", pemErr)
		return "", pemErr
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.parse_certificate_failed", "identity", signingIdentity, "err", err)
		return "", fmt.Errorf("parse local certificate for %q: %w", signingIdentity, err)
	}
	return strings.ToUpper(certificate.SerialNumber.Text(16)), nil
}

func (c *Client) findBundleID(ctx context.Context, identifier string) (*bundleIDRecord, error) {
	query := url.Values{}
	query.Set("filter[identifier]", identifier)
	query.Set("fields[bundleIds]", "identifier,name")
	responseBody, err := c.doJSON(ctx, http.MethodGet, baseURL+"/bundleIds", query, nil)
	if err != nil {
		return nil, err
	}
	var envelope bundleIDListEnvelope
	if err := json.Unmarshal(responseBody, &envelope); err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.decode_bundle_ids_failed", "err", err)
		return nil, fmt.Errorf("decode bundle IDs response: %w", err)
	}
	if len(envelope.Data) == 0 {
		return nil, nil
	}
	return &envelope.Data[0], nil
}

func (c *Client) doJSON(ctx context.Context, method, rawURL string, query url.Values, body []byte) ([]byte, error) {
	requestURL := rawURL
	if len(query) > 0 {
		requestURL = rawURL + "?" + query.Encode()
	}
	var bodyReader io.Reader
	if len(body) > 0 {
		bodyReader = strings.NewReader(string(body))
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, bodyReader)
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.create_request_failed", "method", method, "url", rawURL, "err", err)
		return nil, fmt.Errorf("create request %s %s: %w", method, rawURL, err)
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.request_failed", "method", method, "url", rawURL, "err", err)
		return nil, fmt.Errorf("%s %s: %w", method, rawURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	responseBytes, err := io.ReadAll(response.Body)
	if err != nil {
		appleportalLog.ErrorContext(ctx, "appleportal.read_response_failed", "method", method, "url", rawURL, "err", err)
		return nil, fmt.Errorf("read %s %s response: %w", method, rawURL, err)
	}
	if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
		return responseBytes, nil
	}
	var apiErr apiErrorResponse
	if json.Unmarshal(responseBytes, &apiErr) == nil && len(apiErr.Errors) > 0 {
		first := apiErr.Errors[0]
		detailErr := fmt.Errorf("%s %s returned %s %s: %s", method, rawURL, first.Status, first.Code, first.Detail)
		appleportalLog.ErrorContext(ctx, "appleportal.api_error",
			"method", method, "url", rawURL, "status", first.Status, "code", first.Code, "detail", first.Detail, "err", detailErr)
		return nil, detailErr
	}
	httpErr := fmt.Errorf("%s %s returned HTTP %d: %s", method, rawURL, response.StatusCode, strings.TrimSpace(string(responseBytes)))
	appleportalLog.ErrorContext(ctx, "appleportal.api_http_error", "method", method, "url", rawURL, "http_status", response.StatusCode, "err", httpErr)
	return nil, httpErr
}
