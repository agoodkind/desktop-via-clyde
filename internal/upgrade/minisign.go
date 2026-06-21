package upgrade

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

const (
	minisignAlgorithmSize     = 2
	minisignKeyIDSize         = 8
	minisignPublicKeySize     = 42
	minisignSignatureBlobSize = 74
	minisignPublicKeyPrefix   = "Ed"
	minisignLegacyAlgorithm   = "Ed"
	minisignPrehashAlgorithm  = "ED"
	trustedCommentPrefix      = "trusted comment: "
)

type minisignSignature struct {
	blob            []byte
	keyID           []byte
	signature       []byte
	algorithm       string
	trustedComment  []byte
	globalSignature []byte
}

func verifyMinisign(ctx context.Context, publicKeyLine string, data []byte, signatureFile []byte) error {
	algorithm, err := minisignSignatureAlgorithm(ctx, signatureFile)
	if err != nil {
		return err
	}
	switch algorithm {
	case minisignPrehashAlgorithm:
		digest := blake2b.Sum512(data)
		return verifyMinisignDigest(ctx, publicKeyLine, digest[:], signatureFile)
	case minisignLegacyAlgorithm:
		return verifyMinisignMessage(ctx, publicKeyLine, data, signatureFile, minisignLegacyAlgorithm)
	default:
		return fmt.Errorf("minisign signature algorithm %q is unsupported", algorithm)
	}
}

func verifyMinisignDigest(ctx context.Context, publicKeyLine string, digest []byte, signatureFile []byte) error {
	if len(digest) != blake2b.Size {
		return fmt.Errorf("minisign digest has %d bytes, want %d", len(digest), blake2b.Size)
	}
	return verifyMinisignMessage(ctx, publicKeyLine, digest, signatureFile, minisignPrehashAlgorithm)
}

func verifyMinisignMessage(
	ctx context.Context,
	publicKeyLine string,
	message []byte,
	signatureFile []byte,
	expectedAlgorithm string,
) error {
	publicKey, publicKeyID, err := decodeMinisignPublicKey(ctx, publicKeyLine)
	if err != nil {
		return err
	}
	decodedSignature, err := decodeMinisignSignature(ctx, signatureFile)
	if err != nil {
		return err
	}
	if !bytes.Equal(decodedSignature.keyID, publicKeyID) {
		return fmt.Errorf("minisign key id mismatch")
	}
	if decodedSignature.algorithm != expectedAlgorithm {
		return fmt.Errorf("minisign signature algorithm %q does not match expected %q", decodedSignature.algorithm, expectedAlgorithm)
	}
	if !ed25519.Verify(publicKey, message, decodedSignature.signature) {
		return fmt.Errorf("minisign payload signature verification failed")
	}
	globalMessage := make([]byte, 0, len(decodedSignature.blob)+len(decodedSignature.trustedComment))
	globalMessage = append(globalMessage, decodedSignature.blob...)
	globalMessage = append(globalMessage, decodedSignature.trustedComment...)
	if !ed25519.Verify(publicKey, globalMessage, decodedSignature.globalSignature) {
		return fmt.Errorf("minisign trusted comment signature verification failed")
	}
	return nil
}

func minisignSignatureAlgorithm(ctx context.Context, signatureFile []byte) (string, error) {
	decodedSignature, err := decodeMinisignSignature(ctx, signatureFile)
	if err != nil {
		return "", err
	}
	return decodedSignature.algorithm, nil
}

func decodeMinisignPublicKey(ctx context.Context, publicKeyLine string) (ed25519.PublicKey, []byte, error) {
	line, err := lastNonEmptyLine(publicKeyLine)
	if err != nil {
		return nil, nil, err
	}
	decoded, err := base64.StdEncoding.DecodeString(line)
	if err != nil {
		return nil, nil, logUpgradeError(ctx, "upgrade.minisign_pubkey_base64", fmt.Errorf("decode minisign public key: %w", err))
	}
	if len(decoded) != minisignPublicKeySize {
		return nil, nil, fmt.Errorf("minisign public key has %d bytes, want %d", len(decoded), minisignPublicKeySize)
	}
	algorithm := string(decoded[:minisignAlgorithmSize])
	if algorithm != minisignPublicKeyPrefix {
		return nil, nil, fmt.Errorf("minisign public key algorithm %q is unsupported", algorithm)
	}
	keyID := append([]byte(nil), decoded[minisignAlgorithmSize:minisignAlgorithmSize+minisignKeyIDSize]...)
	publicKey := append(ed25519.PublicKey(nil), decoded[minisignAlgorithmSize+minisignKeyIDSize:]...)
	return publicKey, keyID, nil
}

func decodeMinisignSignature(ctx context.Context, signatureFile []byte) (minisignSignature, error) {
	lines := minisignLines(string(signatureFile))
	if len(lines) != 4 {
		return minisignSignature{}, fmt.Errorf("minisign signature file has %d non-empty lines, want 4", len(lines))
	}
	signatureBlob, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[1]))
	if err != nil {
		return minisignSignature{}, logUpgradeError(ctx, "upgrade.minisign_sig_base64", fmt.Errorf("decode minisign signature blob: %w", err))
	}
	if len(signatureBlob) != minisignSignatureBlobSize {
		return minisignSignature{}, fmt.Errorf("minisign signature blob has %d bytes, want %d", len(signatureBlob), minisignSignatureBlobSize)
	}
	algorithm := string(signatureBlob[:minisignAlgorithmSize])
	if algorithm != minisignPrehashAlgorithm && algorithm != minisignLegacyAlgorithm {
		return minisignSignature{}, fmt.Errorf("minisign signature algorithm %q is unsupported", algorithm)
	}
	trustedComment, err := parseTrustedComment(lines[2])
	if err != nil {
		return minisignSignature{}, err
	}
	globalSignature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(lines[3]))
	if err != nil {
		return minisignSignature{}, logUpgradeError(ctx, "upgrade.minisign_global_sig_base64", fmt.Errorf("decode minisign trusted comment signature: %w", err))
	}
	if len(globalSignature) != ed25519.SignatureSize {
		return minisignSignature{}, fmt.Errorf("minisign trusted comment signature has %d bytes, want %d", len(globalSignature), ed25519.SignatureSize)
	}
	signatureKeyID := append([]byte(nil), signatureBlob[minisignAlgorithmSize:minisignAlgorithmSize+minisignKeyIDSize]...)
	signature := append([]byte(nil), signatureBlob[minisignAlgorithmSize+minisignKeyIDSize:]...)
	return minisignSignature{
		blob:            signatureBlob,
		keyID:           signatureKeyID,
		signature:       signature,
		algorithm:       algorithm,
		trustedComment:  trustedComment,
		globalSignature: globalSignature,
	}, nil
}

func parseTrustedComment(line string) ([]byte, error) {
	if !strings.HasPrefix(line, trustedCommentPrefix) {
		return nil, fmt.Errorf("minisign trusted comment line is missing %q prefix", trustedCommentPrefix)
	}
	return []byte(strings.TrimPrefix(line, trustedCommentPrefix)), nil
}

func lastNonEmptyLine(value string) (string, error) {
	lines := minisignLines(value)
	if len(lines) == 0 {
		return "", fmt.Errorf("minisign public key is empty")
	}
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

func minisignLines(value string) []string {
	normalized := strings.ReplaceAll(value, "\r\n", "\n")
	rawLines := strings.Split(normalized, "\n")
	lines := make([]string, 0, len(rawLines))
	for _, line := range rawLines {
		cleaned := strings.TrimRight(line, "\r")
		if strings.TrimSpace(cleaned) == "" {
			continue
		}
		lines = append(lines, cleaned)
	}
	return lines
}
