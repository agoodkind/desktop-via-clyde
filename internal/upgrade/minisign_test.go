package upgrade

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"golang.org/x/crypto/blake2b"
)

func TestVerifyMinisignAcceptsPrehashedSignature(t *testing.T) {
	payload := []byte("signed test payload")
	publicKeyLine, signatureFile := minisignFixture(t, payload)

	if err := verifyMinisign(context.Background(), publicKeyLine, payload, signatureFile); err != nil {
		t.Fatalf("verifyMinisign: %v", err)
	}
}

func TestVerifyMinisignRejectsMutatedPayload(t *testing.T) {
	payload := []byte("signed test payload")
	publicKeyLine, signatureFile := minisignFixture(t, payload)
	mutatedPayload := append([]byte(nil), payload...)
	mutatedPayload[0] ^= 0xff

	if err := verifyMinisign(context.Background(), publicKeyLine, mutatedPayload, signatureFile); err == nil {
		t.Fatal("verifyMinisign should reject a mutated payload")
	}
}

func TestVerifyMinisignRejectsKeyIDMismatch(t *testing.T) {
	payload := []byte("signed test payload")
	publicKeyLine, signatureFile := minisignFixture(t, payload)
	mutatedSignatureFile := mutateSignatureKeyID(t, signatureFile)

	if err := verifyMinisign(context.Background(), publicKeyLine, payload, mutatedSignatureFile); err == nil {
		t.Fatal("verifyMinisign should reject a key id mismatch")
	}
}

func minisignFixture(t *testing.T, payload []byte) (string, []byte) {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = 0x42
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID := []byte{0xfe, 0xb4, 0xc3, 0xf6, 0x72, 0x86, 0xa7, 0x8f}
	publicKeyBytes := make([]byte, 0, minisignPublicKeySize)
	publicKeyBytes = append(publicKeyBytes, []byte(minisignPublicKeyPrefix)...)
	publicKeyBytes = append(publicKeyBytes, keyID...)
	publicKeyBytes = append(publicKeyBytes, publicKey...)
	publicKeyLine := "untrusted comment: minisign public key\n" + base64.StdEncoding.EncodeToString(publicKeyBytes)

	digest := blake2b.Sum512(payload)
	signature := ed25519.Sign(privateKey, digest[:])
	signatureBlob := make([]byte, 0, minisignSignatureBlobSize)
	signatureBlob = append(signatureBlob, []byte(minisignPrehashAlgorithm)...)
	signatureBlob = append(signatureBlob, keyID...)
	signatureBlob = append(signatureBlob, signature...)

	trustedComment := []byte("timestamp:1\tfile:test")
	globalMessage := make([]byte, 0, len(signatureBlob)+len(trustedComment))
	globalMessage = append(globalMessage, signatureBlob...)
	globalMessage = append(globalMessage, trustedComment...)
	globalSignature := ed25519.Sign(privateKey, globalMessage)
	lines := []string{
		"untrusted comment: signature from test key",
		base64.StdEncoding.EncodeToString(signatureBlob),
		"trusted comment: " + string(trustedComment),
		base64.StdEncoding.EncodeToString(globalSignature),
	}
	return publicKeyLine, []byte(strings.Join(lines, "\n"))
}

func mutateSignatureKeyID(t *testing.T, signatureFile []byte) []byte {
	t.Helper()
	lines := strings.Split(string(signatureFile), "\n")
	signatureBlob, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		t.Fatalf("DecodeString signature blob: %v", err)
	}
	signatureBlob[minisignAlgorithmSize] ^= 0xff
	lines[1] = base64.StdEncoding.EncodeToString(signatureBlob)
	return []byte(strings.Join(lines, "\n"))
}
