// Package apikey defines the domain model for API key authentication.
//
// API keys are a second authentication credential type alongside JWTs.
// They are long-lived, machine-to-machine credentials suited for:
//   - CI/CD pipelines (GitHub Actions, Jenkins)
//   - Server-to-server integrations
//   - Automation scripts
//
// Security model:
//
//	Raw key format:  urlsk_{workspacePrefix}_{randomPart}
//	Example:         urlsk_ab1cde2f3g4h5i6j7k8l9m0n1o2p3q4r
//
//	The "urlsk_" prefix lets users and security scanners (e.g. GitHub
//	secret scanning) identify leaked keys quickly. GitHub's secret
//	scanning program accepts custom token patterns from API providers.
//
//	Storage: only bcrypt(sha256(rawKey)) is persisted.
//	The raw key is shown once at creation and then irrecoverable.
//
// Authentication flow:
//  1. Client sends key in Authorization header: "Bearer urlsk_ab1cde2f..."
//     OR in X-API-Key header: "urlsk_ab1cde2f..."
//  2. Middleware extracts the 8-char prefix after "urlsk_"
//  3. DB lookup by prefix (fast index scan, usually 1 row)
//  4. bcrypt.Compare(stored_hash, sha256(submitted_key))
//  5. Check revoked_at and expires_at
//  6. Inject synthetic Claims into context (same shape as JWT claims)
package apikey

import (
	"strings"
	"time"
)

const (
	// KeyPrefix is the static prefix for all API keys.
	// Enables automatic secret scanning by GitHub and similar tools.
	KeyPrefix = "urlsk_"

	// RawKeyPrefixLength is the number of chars stored as key_prefix in DB.
	// "urlsk_" (6) + 8 alphanumeric = 14 chars total.
	RawKeyPrefixLength = 14

	// ValidScopes defines the allowed scope values.
	// Stored as a PostgreSQL TEXT array in the database.
	ScopeRead  = "read"
	ScopeWrite = "write"
	ScopeAdmin = "admin"
)

// APIKey represents a stored API key record.
// The RawKey field is NEVER persisted — it is set only at creation time
// so the application layer can return it to the user once.
type APIKey struct {
	ID          string   // ULID
	WorkspaceID string   // FK → workspaces.id
	Name        string   // Human-readable label
	KeyHash     string   // bcrypt(sha256(rawKey)) — stored credential
	KeyPrefix   string   // First 14 chars of raw key (display only)
	Scopes      []string // ["read", "write"] etc.
	CreatedBy   string   // User ULID
	CreatedAt   time.Time
	ExpiresAt   *time.Time // nil = no expiry
	RevokedAt   *time.Time // nil = active
	LastUsedAt  *time.Time // nil = never used

	// RawKey is populated ONLY at creation time — never loaded from DB.
	// It is the caller's responsibility to return this to the user and
	// never persist it beyond the single HTTP response.
	RawKey string `db:"-"` // db:"-" signals to any ORM to skip this field
}

// IsActive returns true if the key has not been revoked and has not expired.
func (k *APIKey) IsActive() bool {
	if k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && time.Now().UTC().After(*k.ExpiresAt) {
		return false
	}
	return true
}

// HasScope returns true if the key was issued with the given scope.
func (k *APIKey) HasScope(scope string) bool {
	for _, s := range k.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// ScopeString returns the scopes as a space-separated string.
// This matches the JWT "scope" claim format, allowing the auth
// middleware to treat API key claims identically to JWT claims.
func (k *APIKey) ScopeString() string {
	return strings.Join(k.Scopes, " ")
}

// ExtractPrefix extracts the key_prefix from a raw key string.
// Returns empty string if the key is too short or missing the urlsk_ prefix.
func ExtractPrefix(rawKey string) string {
	if len(rawKey) < RawKeyPrefixLength {
		return ""
	}
	if !strings.HasPrefix(rawKey, KeyPrefix) {
		return ""
	}
	return rawKey[:RawKeyPrefixLength]
}

// ValidateScopes returns an error message if any scope value is invalid.
// Returns empty string if all scopes are valid.
func ValidateScopes(scopes []string) string {
	valid := map[string]bool{ScopeRead: true, ScopeWrite: true, ScopeAdmin: true}
	for _, s := range scopes {
		if !valid[s] {
			return "invalid scope: " + s + " (must be one of: read, write, admin)"
		}
	}
	if len(scopes) == 0 {
		return "at least one scope is required"
	}
	return ""
}
