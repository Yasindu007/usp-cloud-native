// Package iphasher implements privacy-preserving IP address hashing.
//
// GDPR compliance (PRD section 14.1):
//
//	"IP addresses captured in redirect events MUST be hashed immediately
//	on ingestion using SHA-256 with a daily rotating salt. Raw IPs MUST
//	NOT be stored."
//
// Algorithm:
//
//	hash = hex( SHA-256( ip_address + ":" + daily_salt ) )
//
// Daily salt rotation:
//
//	The salt changes every calendar day (UTC). This means:
//	- Same IP on the same day → same hash (daily unique visitor counting)
//	- Same IP on different days → different hashes (no cross-day tracking)
//	- Yesterday's hashes cannot be reversed to find today's IP (key separation)
//
// Salt derivation:
//
//	salt = hex( SHA-256( secret_key + ":" + YYYY-MM-DD ) )
//	The secret_key is loaded from environment (IP_HASH_SALT env var).
//	Without the secret_key, hashes cannot be reversed even with the date.
//	With only the date and no secret_key, an attacker cannot reproduce hashes.
//
// Why not HMAC?
//
//	SHA-256(key || message) is vulnerable to length-extension attacks.
//	For this use case (hashing, not MAC), SHA-256(key + ":" + date + ":" + ip)
//	with a fixed separator is sufficient and simpler. If MAC properties
//	are needed, replace with HMAC-SHA256.
//
// Performance:
//
//	sha256.Sum256 allocates one [32]byte on the stack (no heap allocation).
//	hex.EncodeToString allocates a string. At 10k RPS: ~10k string allocs/s.
//	Acceptable. If profiling shows this as a bottleneck, use a sync.Pool
//	of hex-encoding buffers.
package iphasher

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
)

// Hasher computes daily-salted IP hashes.
// Create once at startup and share across goroutines — it is safe for
// concurrent use (no mutable state).
type Hasher struct {
	secretKey string
}

// New creates a Hasher with the given secret key.
// secretKey should be a high-entropy random string loaded from environment.
// If secretKey is empty, hashing still works but provides weaker privacy
// (hashes are reproducible by anyone who knows the date).
func New(secretKey string) *Hasher {
	return &Hasher{secretKey: secretKey}
}

// Hash returns a daily-salted SHA-256 hash of the IP address.
// Returns an empty string for empty IPs (bot traffic, synthetic requests).
//
// The returned hash is 64 hex characters (SHA-256 = 32 bytes = 64 hex chars).
func (h *Hasher) Hash(ip string) string {
	if ip == "" {
		return ""
	}

	// Build the salt for today (UTC date).
	// The salt changes at UTC midnight — all events within a UTC day share a salt.
	today := time.Now().UTC().Format("2006-01-02")
	saltInput := h.secretKey + ":" + today

	saltSum := sha256.Sum256([]byte(saltInput))
	salt := hex.EncodeToString(saltSum[:])

	// Hash: SHA-256(ip + ":" + daily_salt)
	payload := ip + ":" + salt
	hashSum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hashSum[:])
}

// HashWithDate hashes an IP for a specific date (UTC).
// Used in testing and for re-processing historical events.
// NOT used in the redirect hot path (which always uses today's date).
func (h *Hasher) HashWithDate(ip string, date time.Time) string {
	if ip == "" {
		return ""
	}
	day := date.UTC().Format("2006-01-02")
	saltInput := h.secretKey + ":" + day
	saltSum := sha256.Sum256([]byte(saltInput))
	salt := hex.EncodeToString(saltSum[:])

	payload := ip + ":" + salt
	hashSum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hashSum[:])
}
