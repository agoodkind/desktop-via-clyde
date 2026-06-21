package upgrade

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// minisignLegacyFixture builds a correct legacy ("Ed") minisign signature, which
// signs the raw message rather than a blake2b prehash. The legacy verify branch
// has no live golden vector because modern minisign always prehashes, so this
// synthetic-but-correct vector guards that code path.
func minisignLegacyFixture(t *testing.T, payload []byte) (string, []byte) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = 0x24
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID := []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}

	publicKeyBytes := make([]byte, 0, minisignPublicKeySize)
	publicKeyBytes = append(publicKeyBytes, []byte(minisignPublicKeyPrefix)...)
	publicKeyBytes = append(publicKeyBytes, keyID...)
	publicKeyBytes = append(publicKeyBytes, publicKey...)
	publicKeyLine := "untrusted comment: minisign public key\n" + base64.StdEncoding.EncodeToString(publicKeyBytes)

	signature := ed25519.Sign(privateKey, payload)
	signatureBlob := make([]byte, 0, minisignSignatureBlobSize)
	signatureBlob = append(signatureBlob, []byte(minisignLegacyAlgorithm)...)
	signatureBlob = append(signatureBlob, keyID...)
	signatureBlob = append(signatureBlob, signature...)

	trustedComment := []byte("timestamp:2\tfile:legacy")
	globalMessage := make([]byte, 0, len(signature)+len(trustedComment))
	globalMessage = append(globalMessage, signature...)
	globalMessage = append(globalMessage, trustedComment...)
	globalSignature := ed25519.Sign(privateKey, globalMessage)

	lines := []string{
		"untrusted comment: legacy signature",
		base64.StdEncoding.EncodeToString(signatureBlob),
		"trusted comment: " + string(trustedComment),
		base64.StdEncoding.EncodeToString(globalSignature),
	}
	return publicKeyLine, []byte(strings.Join(lines, "\n"))
}

func TestVerifyMinisignAcceptsLegacySignature(t *testing.T) {
	payload := []byte("legacy signed payload")
	publicKeyLine, signatureFile := minisignLegacyFixture(t, payload)
	if err := verifyMinisign(context.Background(), publicKeyLine, payload, signatureFile); err != nil {
		t.Fatalf("verifyMinisign legacy: %v", err)
	}
}

func TestVerifyMinisignLegacyRejectsMutatedPayload(t *testing.T) {
	payload := []byte("legacy signed payload")
	publicKeyLine, signatureFile := minisignLegacyFixture(t, payload)
	mutated := append([]byte(nil), payload...)
	mutated[0] ^= 0xff
	if err := verifyMinisign(context.Background(), publicKeyLine, mutated, signatureFile); err == nil {
		t.Fatal("verifyMinisign should reject a mutated legacy payload")
	}
}

func TestSafeExtractPathRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	rejected := []string{
		"../escape",
		"../../etc/passwd",
		"/absolute/path",
		"a/../../b",
		"..",
	}
	for _, name := range rejected {
		if _, err := safeExtractPath(context.Background(), root, name); err == nil {
			t.Errorf("safeExtractPath(%q) should be rejected", name)
		} else if !errors.Is(err, errTarUnsafePath) {
			t.Errorf("safeExtractPath(%q) error = %v, want errTarUnsafePath", name, err)
		}
	}
	allowed := []string{
		"Conductor.app/Contents/Info.plist",
		"a/b/c",
		"a/../b",
	}
	for _, name := range allowed {
		if _, err := safeExtractPath(context.Background(), root, name); err != nil {
			t.Errorf("safeExtractPath(%q) should be allowed: %v", name, err)
		}
	}
}
