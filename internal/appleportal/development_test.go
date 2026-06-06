package appleportal

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// fakeASC is an in-memory App Store Connect double. It records the POST counts per
// resource so a test can prove the find-existing-first idempotent path skips
// creation on a second run, and it captures the last create body so a test can
// assert the request shape. It never contacts Apple.
type fakeASC struct {
	mu sync.Mutex

	devices    map[string]deviceRecord      // udid -> record
	bundleIDs  map[string]bundleIDRecord    // identifier -> record
	certs      map[string]certificateRecord // serial -> record
	profiles   map[string]profileRecord     // name -> record
	certDER    []byte
	profilePEM []byte

	postDevices  int
	postBundles  int
	postCerts    int
	postProfiles int

	lastCertBody    map[string]any
	lastProfileBody map[string]any
	lastDeviceBody  map[string]any

	nextID  int
	nextSer int
}

func newFakeASC() *fakeASC {
	return &fakeASC{
		devices:    map[string]deviceRecord{},
		bundleIDs:  map[string]bundleIDRecord{},
		certs:      map[string]certificateRecord{},
		profiles:   map[string]profileRecord{},
		certDER:    []byte("fake-development-certificate-der"),
		profilePEM: []byte("fake-mobileprovision-bytes"),
	}
}

func (f *fakeASC) id(prefix string) string {
	f.nextID++
	return prefix + "-" + itoa(f.nextID)
}

func (f *fakeASC) serial() string {
	f.nextSer++
	return "DEVSERIAL" + itoa(f.nextSer)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}

func (f *fakeASC) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		path := r.URL.Path
		switch {
		case path == "/devices" && r.Method == http.MethodGet:
			f.listDevices(w, r)
		case path == "/devices" && r.Method == http.MethodPost:
			f.createDevice(w, r)
		case path == "/bundleIds" && r.Method == http.MethodGet:
			f.listBundleIDs(w, r)
		case path == "/bundleIds" && r.Method == http.MethodPost:
			f.createBundleID(w, r)
		case path == "/certificates" && r.Method == http.MethodGet:
			f.listCerts(w)
		case path == "/certificates" && r.Method == http.MethodPost:
			f.createCert(w, r)
		case path == "/profiles" && r.Method == http.MethodGet:
			f.listProfiles(w, r)
		case path == "/profiles" && r.Method == http.MethodPost:
			f.createProfile(w, r)
		case strings.HasPrefix(path, "/profiles/") && r.Method == http.MethodGet:
			f.downloadProfile(w)
		default:
			http.Error(w, "unexpected request "+r.Method+" "+path, http.StatusNotFound)
		}
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	body, _ := json.Marshal(value)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func decodeBody(r *http.Request) map[string]any {
	raw, _ := io.ReadAll(r.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out
}

func (f *fakeASC) listDevices(w http.ResponseWriter, r *http.Request) {
	udid := r.URL.Query().Get("filter[udid]")
	records := []deviceRecord{}
	if rec, ok := f.devices[udid]; ok {
		records = append(records, rec)
	}
	writeJSON(w, deviceListEnvelope{Data: records})
}

func (f *fakeASC) createDevice(w http.ResponseWriter, r *http.Request) {
	f.postDevices++
	f.lastDeviceBody = decodeBody(r)
	udid := digString(f.lastDeviceBody, "udid")
	rec := deviceRecord{ID: f.id("device"), Type: "devices", Attributes: deviceAttributes{
		Name: digString(f.lastDeviceBody, "name"), UDID: udid, Platform: macOSPlatform,
	}}
	f.devices[udid] = rec
	writeJSON(w, deviceEnvelope{Data: rec})
}

func (f *fakeASC) listBundleIDs(w http.ResponseWriter, r *http.Request) {
	identifier := r.URL.Query().Get("filter[identifier]")
	records := []bundleIDRecord{}
	if rec, ok := f.bundleIDs[identifier]; ok {
		records = append(records, rec)
	}
	writeJSON(w, bundleIDListEnvelope{Data: records})
}

func (f *fakeASC) createBundleID(w http.ResponseWriter, r *http.Request) {
	f.postBundles++
	body := decodeBody(r)
	identifier := digString(body, "identifier")
	rec := bundleIDRecord{ID: f.id("bundle"), Type: "bundleIds", Attributes: bundleIDAttributes{
		Identifier: identifier, Name: digString(body, "name"),
	}}
	f.bundleIDs[identifier] = rec
	writeJSON(w, bundleIDEnvelope{Data: rec})
}

func (f *fakeASC) listCerts(w http.ResponseWriter) {
	records := make([]certificateRecord, 0, len(f.certs))
	for _, rec := range f.certs {
		records = append(records, rec)
	}
	writeJSON(w, certificateListEnvelope{Data: records})
}

func (f *fakeASC) createCert(w http.ResponseWriter, r *http.Request) {
	f.postCerts++
	f.lastCertBody = decodeBody(r)
	serial := f.serial()
	rec := certificateRecord{ID: f.id("cert"), Type: "certificates", Attributes: certificateAttributes{
		Name: "Apple Development", SerialNumber: serial, CertificateType: developmentCertType,
	}}
	f.certs[serial] = rec
	writeJSON(w, certificateContentEnvelope{Data: certificateContentRecord{
		ID: rec.ID, Type: "certificates", Attributes: certificateContentAttributes{
			Name: rec.Attributes.Name, SerialNumber: serial, CertificateType: developmentCertType,
			CertificateContent: base64.StdEncoding.EncodeToString(f.certDER),
		},
	}})
}

func (f *fakeASC) listProfiles(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("filter[name]")
	records := []profileRecord{}
	if rec, ok := f.profiles[name]; ok {
		records = append(records, rec)
	}
	writeJSON(w, profileListEnvelope{Data: records})
}

func (f *fakeASC) createProfile(w http.ResponseWriter, r *http.Request) {
	f.postProfiles++
	f.lastProfileBody = decodeBody(r)
	name := ""
	if data, ok := f.lastProfileBody["data"].(map[string]any); ok {
		if attrs, ok := data["attributes"].(map[string]any); ok {
			name, _ = attrs["name"].(string)
		}
	}
	rec := profileRecord{ID: f.id("profile"), Type: "profiles", Attributes: profileAttributes{
		Name: name, ProfileType: developmentProfileType,
	}}
	f.profiles[name] = rec
	writeJSON(w, profileEnvelope{Data: rec})
}

func (f *fakeASC) downloadProfile(w http.ResponseWriter) {
	writeJSON(w, profileEnvelope{Data: profileRecord{
		ID: "profile-download", Type: "profiles", Attributes: profileAttributes{
			ProfileContent: base64.StdEncoding.EncodeToString(f.profilePEM),
		},
	}})
}

// digString reads data.attributes.<key> from a decoded create request body.
func digString(body map[string]any, key string) string {
	data, ok := body["data"].(map[string]any)
	if !ok {
		return ""
	}
	attrs, ok := data["attributes"].(map[string]any)
	if !ok {
		return ""
	}
	value, _ := attrs[key].(string)
	return value
}

func newTestClient(serverURL string) *Client {
	return &Client{httpClient: &http.Client{}, token: "test-token", baseURL: serverURL}
}

// fakeOpenssl stands in for the openssl shell-out: it writes a stub p12 at the
// path following the -out flag so the surrounding file bookkeeping succeeds without
// invoking a real binary.
func fakeOpenssl(_ context.Context, _ string, args ...string) error {
	for i, arg := range args {
		if arg == "-out" && i+1 < len(args) {
			return os.WriteFile(args[i+1], []byte("fake-p12"), 0o600)
		}
	}
	return nil
}

func TestCreateDevelopmentCertificateRequestShape(t *testing.T) {
	fake := newFakeASC()
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := newTestClient(server.URL)

	cert, der, err := client.CreateDevelopmentCertificate(context.Background(), "csr-pem-content")
	if err != nil {
		t.Fatalf("CreateDevelopmentCertificate: %v", err)
	}
	if got := digString(fake.lastCertBody, "certificateType"); got != developmentCertType {
		t.Fatalf("certificateType = %q, want %q", got, developmentCertType)
	}
	if got := digString(fake.lastCertBody, "csrContent"); got != "csr-pem-content" {
		t.Fatalf("csrContent = %q, want csr-pem-content", got)
	}
	if cert.SerialNumber == "" {
		t.Fatalf("expected a serial number in the returned certificate")
	}
	if string(der) != string(fake.certDER) {
		t.Fatalf("decoded DER = %q, want %q", der, fake.certDER)
	}
}

func TestEnsureDevelopmentProfileRequestShape(t *testing.T) {
	fake := newFakeASC()
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := newTestClient(server.URL)

	if _, err := client.EnsureDevelopmentProfile(context.Background(), "dvc dev", "bundle-1", "cert-1", "device-1"); err != nil {
		t.Fatalf("EnsureDevelopmentProfile: %v", err)
	}
	if fake.postProfiles != 1 {
		t.Fatalf("expected one profile POST, got %d", fake.postProfiles)
	}
	data, _ := fake.lastProfileBody["data"].(map[string]any)
	attrs, _ := data["attributes"].(map[string]any)
	if attrs["profileType"] != developmentProfileType {
		t.Fatalf("profileType = %v, want %q", attrs["profileType"], developmentProfileType)
	}
	rels, _ := data["relationships"].(map[string]any)
	if _, ok := rels["devices"]; !ok {
		t.Fatalf("development profile request missing devices relationship: %v", rels)
	}
	if _, ok := rels["certificates"]; !ok {
		t.Fatalf("development profile request missing certificates relationship: %v", rels)
	}
}

func TestEnsureDevelopmentProfileIdempotent(t *testing.T) {
	fake := newFakeASC()
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := newTestClient(server.URL)

	first, err := client.EnsureDevelopmentProfile(context.Background(), "dvc dev", "bundle-1", "cert-1", "device-1")
	if err != nil {
		t.Fatalf("EnsureDevelopmentProfile (first): %v", err)
	}
	second, err := client.EnsureDevelopmentProfile(context.Background(), "dvc dev", "bundle-1", "cert-1", "device-1")
	if err != nil {
		t.Fatalf("EnsureDevelopmentProfile (second): %v", err)
	}
	if fake.postProfiles != 1 {
		t.Fatalf("expected exactly one profile POST across two calls, got %d", fake.postProfiles)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotent profile IDs differ: %q vs %q", first.ID, second.ID)
	}
}

func TestEnsureDevelopmentDeviceIdempotent(t *testing.T) {
	fake := newFakeASC()
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := newTestClient(server.URL)

	if _, err := client.EnsureDevelopmentDevice(context.Background(), "mac", "UDID-123"); err != nil {
		t.Fatalf("EnsureDevelopmentDevice (first): %v", err)
	}
	if _, err := client.EnsureDevelopmentDevice(context.Background(), "mac", "UDID-123"); err != nil {
		t.Fatalf("EnsureDevelopmentDevice (second): %v", err)
	}
	if fake.postDevices != 1 {
		t.Fatalf("expected exactly one device POST across two calls, got %d", fake.postDevices)
	}
	if got := digString(fake.lastDeviceBody, "udid"); got != "UDID-123" {
		t.Fatalf("device udid = %q, want UDID-123", got)
	}
}

func TestFindDevelopmentCertificateMatchesSerial(t *testing.T) {
	fake := newFakeASC()
	fake.certs["AAA111"] = certificateRecord{ID: "cert-aaa", Attributes: certificateAttributes{
		SerialNumber: "AAA111", CertificateType: developmentCertType,
	}}
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := newTestClient(server.URL)

	found, err := client.FindDevelopmentCertificate(context.Background(), "aaa111")
	if err != nil {
		t.Fatalf("FindDevelopmentCertificate: %v", err)
	}
	if found == nil || found.ID != "cert-aaa" {
		t.Fatalf("expected cert-aaa by case-insensitive serial, got %v", found)
	}
	missing, err := client.FindDevelopmentCertificate(context.Background(), "ZZZ999")
	if err != nil {
		t.Fatalf("FindDevelopmentCertificate (missing): %v", err)
	}
	if missing != nil {
		t.Fatalf("expected nil for unknown serial, got %v", missing)
	}
}

func TestGenerateDevelopmentAssetsIdempotentReuse(t *testing.T) {
	fake := newFakeASC()
	server := httptest.NewServer(fake.handler())
	defer server.Close()
	client := newTestClient(server.URL)

	destDir := t.TempDir()
	opts := DevelopmentAssetOptions{
		DeviceUDID: "UDID-REUSE",
		DestDir:    destDir,
		client:     client,
		run:        fakeOpenssl,
	}

	first, err := GenerateDevelopmentAssets(context.Background(), opts)
	if err != nil {
		t.Fatalf("GenerateDevelopmentAssets (first): %v", err)
	}
	if first.ReusedCert {
		t.Fatalf("first run should mint a fresh certificate, not reuse")
	}
	for _, path := range []string{first.ProfilePath, first.P12Path, first.P12PasswordFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected asset written at %s: %v", path, err)
		}
	}
	serialPath := filepath.Join(destDir, defaultAssetBaseName+"-cert-serial.txt")
	if _, err := os.Stat(serialPath); err != nil {
		t.Fatalf("expected recorded serial file: %v", err)
	}

	second, err := GenerateDevelopmentAssets(context.Background(), opts)
	if err != nil {
		t.Fatalf("GenerateDevelopmentAssets (second): %v", err)
	}
	if !second.ReusedCert {
		t.Fatalf("second run should reuse the recorded certificate")
	}
	if fake.postCerts != 1 {
		t.Fatalf("expected exactly one certificate POST across two runs, got %d", fake.postCerts)
	}
	if fake.postDevices != 1 {
		t.Fatalf("expected exactly one device POST across two runs, got %d", fake.postDevices)
	}
	if fake.postProfiles != 1 {
		t.Fatalf("expected exactly one profile POST across two runs, got %d", fake.postProfiles)
	}
	if fake.postBundles != 1 {
		t.Fatalf("expected exactly one bundle id POST across two runs, got %d", fake.postBundles)
	}
}

func TestParseProvisioningUDID(t *testing.T) {
	provisioning := "      Provisioning UDID: 00006000-0011AABBCCDD001E\n      Hardware UUID: 11112222-3333-4444\n"
	got, err := parseProvisioningUDID(provisioning)
	if err != nil {
		t.Fatalf("parseProvisioningUDID: %v", err)
	}
	if got != "00006000-0011AABBCCDD001E" {
		t.Fatalf("provisioning UDID = %q, want the Provisioning UDID line", got)
	}

	hardwareOnly := "      Hardware UUID: 11112222-3333-4444\n"
	got, err = parseProvisioningUDID(hardwareOnly)
	if err != nil {
		t.Fatalf("parseProvisioningUDID (hardware fallback): %v", err)
	}
	if got != "11112222-3333-4444" {
		t.Fatalf("fallback UDID = %q, want the Hardware UUID line", got)
	}

	if _, err := parseProvisioningUDID("no identifiers here\n"); err == nil {
		t.Fatalf("expected an error when neither identifier is present")
	}
}

func TestGenerateDevelopmentCSRProducesPEM(t *testing.T) {
	csrPEM, keyPEM, err := GenerateDevelopmentCSR("desktop-via-clyde development *", "")
	if err != nil {
		t.Fatalf("GenerateDevelopmentCSR: %v", err)
	}
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("expected a CERTIFICATE REQUEST PEM block, got %v", block)
	}
	if _, err := x509.ParseCertificateRequest(block.Bytes); err != nil {
		t.Fatalf("parse generated CSR: %v", err)
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Fatalf("expected a PRIVATE KEY PEM block, got %v", keyBlock)
	}
}
