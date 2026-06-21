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

// Golden vector produced by the reference minisign 0.12 tool (prehashed, the
// algorithm conductor's Tauri updater uses). Reference-verified with
// `minisign -V`. This guards the real wire format against regressions in the
// trusted-comment concatenation; a self-generated fixture cannot, because a bug
// in signing and verifying agree with each other.
const (
	goldenMinisignPublicKey = "RWRQLX8UHSZxXNisvmahvVFHLdzBixj/FI1f9wjUUtEMs3vUmRKGg27O"
	goldenMinisignPayload   = "desktop-via-clyde minisign golden payload\n"
	goldenMinisignSignature = "untrusted comment: signature from minisign secret key\n" +
		"RURQLX8UHSZxXOomjjUJoV1sFKQA3965fqkgKOnJ0S2MvrVm3RxFksLNbxs3ND80yEl2OfWjAAC9ZNs3hxofzNHD4MgCvsP53g0=\n" +
		"trusted comment: golden prehash vector\n" +
		"IB8Aq1G5TY0Pc3IPtfTMVDstBpbFaVCqYJakNsu70grGZ7QERnDZbUeNG9X83DgDtPSChIHhtNIYELM3KeAjDA==\n"
)

func TestVerifyMinisignAcceptsRealMinisignPrehashVector(t *testing.T) {
	err := verifyMinisign(
		context.Background(),
		goldenMinisignPublicKey,
		[]byte(goldenMinisignPayload),
		[]byte(goldenMinisignSignature),
	)
	if err != nil {
		t.Fatalf("verifyMinisign on a real minisign-signed vector: %v", err)
	}
}

func TestVerifyMinisignRejectsRealVectorWithMutatedTrustedComment(t *testing.T) {
	mutated := strings.Replace(goldenMinisignSignature, "golden prehash vector", "tampered comment xxxxx", 1)
	err := verifyMinisign(
		context.Background(),
		goldenMinisignPublicKey,
		[]byte(goldenMinisignPayload),
		[]byte(mutated),
	)
	if err == nil {
		t.Fatal("verifyMinisign should reject a real vector whose trusted comment was mutated")
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
	// Real minisign signs the bare 64-byte signature followed by the trusted
	// comment, not the 74-byte blob. The fixture must match so the test exercises
	// the real wire format rather than agreeing with a verifier bug.
	globalMessage := make([]byte, 0, len(signature)+len(trustedComment))
	globalMessage = append(globalMessage, signature...)
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
