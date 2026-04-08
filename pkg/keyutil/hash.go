// Package keyutil provides cryptographic helpers for API key generation
// and verification.
//
// Hashing pipeline (PRD section 10.1):
//
//	raw_key  ──SHA-256──►  sha256_hex  ──bcrypt(cost=12)──►  stored_hash
//
// Why SHA-256 before bcrypt?
//
//	bcrypt silently truncates inputs longer than 72 bytes. If we bcrypt the
//	raw key directly, keys that differ only after byte 72 would hash to the
//	same value — a subtle collision vulnerability. SHA-256 first collapses
//	all key lengths to 32 bytes (64 hex chars) before bcrypt, eliminating
//	this risk entirely regardless of key length.
//
// Why bcrypt cost=12?
//
//	Cost 12 takes ~250ms per hash on modern hardware. This makes offline
//	dictionary attacks against a leaked hash table require ~250ms per
//	candidate — rendering brute-force economically infeasible. We do not
//	use cost > 12 because API key authentication is not rate-limited the
//	same way password login is, and very high costs would add latency.
//
// Key generation:
//
//	Keys are generated from crypto/rand (OS CSPRNG), same rationale as
//	short code generation — math/rand is predictable.
package keyutil

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

const (
	// BcryptCost is the work factor for bcrypt hashing.
	// OWASP recommendation for bcrypt: cost >= 10. We use 12.
	BcryptCost = 12

	// rawKeyRandomBytes is the number of cryptographically random bytes
	// used for the variable portion of the key. 32 bytes = 256 bits of
	// entropy, hex-encoded to 64 chars.
	rawKeyRandomBytes = 32
)

// GenerateRaw generates a new raw API key.
// Format: urlsk_{8-char-workspace-prefix}{random-portion}
//
// The workspacePrefix is the first 8 chars of the workspace ULID,
// making keys visually identifiable to the workspace they belong to.
// This is purely cosmetic — the prefix is never used for auth.
//
// Example output: urlsk_01HXYZ123456789abcdefghijklmnopqrstuvwxyz01234567
func GenerateRaw(workspaceID string) (string, error) {
	// Take first 8 chars of workspace ID for the identifier portion.
	wsPrefix := workspaceID
	if len(wsPrefix) > 8 {
		wsPrefix = wsPrefix[:8]
	}

	// Generate 32 bytes of cryptographically random data.
	randBytes := make([]byte, rawKeyRandomBytes)
	if _, err := rand.Read(randBytes); err != nil {
		return "", fmt.Errorf("keyutil: generating random bytes: %w", err)
	}

	// Hex-encode for URL safety. Result: 64 alphanumeric chars.
	randHex := hex.EncodeToString(randBytes)

	// Full key: "urlsk_" + wsPrefix (8) + random (64) = 78 chars total
	return "urlsk_" + wsPrefix + randHex, nil
}

// Hash computes bcrypt(sha256(rawKey)).
// This is the value stored in the api_keys.key_hash column.
// Bcrypt output includes the salt and cost factor — the stored hash
// is self-contained and portable.
func Hash(rawKey string) (string, error) {
	// Step 1: SHA-256 to normalise key length before bcrypt.
	digest := sha256sum(rawKey)

	// Step 2: bcrypt with configured cost.
	hashed, err := bcrypt.GenerateFromPassword([]byte(digest), BcryptCost)
	if err != nil {
		return "", fmt.Errorf("keyutil: bcrypt hashing: %w", err)
	}

	return string(hashed), nil
}

// Verify returns true if rawKey matches the stored bcrypt hash.
// This is the verification step during authentication.
// Timing: ~250ms on modern hardware (bcrypt cost=12).
//
// bcrypt.CompareHashAndPassword is constant-time with respect to the
// hash comparison — it does not short-circuit on mismatch, preventing
// timing oracle attacks.
func Verify(rawKey, storedHash string) bool {
	digest := sha256sum(rawKey)
	err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(digest))
	return err == nil
}

// sha256sum returns the hex-encoded SHA-256 digest of the input string.
// The output is always 64 hex characters regardless of input length.
func sha256sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
