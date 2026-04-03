// Package auth defines the authentication domain model.
// It contains the Claims type that flows through the system after
// a JWT is validated — this is the authoritative identity object
// used by all application and domain layer authorization checks.
//
// Clean Architecture placement:
//
//	Claims live in the domain layer because authorization rules
//	("does this user have write scope?") are business rules.
//	The JWT parsing mechanism lives in the infrastructure layer.
//	The middleware wires them together.
package auth

import (
	"context"
	"time"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

const claimsContextKey contextKey = "auth_claims"

// Claims represents the verified identity of an authenticated caller.
// It is populated by the JWT middleware after successful token validation
// and stored in the request context for downstream handlers.
//
// Field mapping from JWT standard claims and custom claims:
//
//	sub          → UserID
//	jti          → TokenID       (used for deny list revocation)
//	iss          → Issuer
//	aud          → Audiences
//	iat          → IssuedAt
//	exp          → ExpiresAt
//	workspace_id → WorkspaceID   (custom claim, required)
//	scope        → Scope         (space-separated list: "read write admin")
//
// Security invariant: Claims is only created by the JWT middleware
// after cryptographic signature verification. No other code in the
// system may construct a Claims value — this prevents privilege escalation
// via context injection.
type Claims struct {
	// UserID is the ULID of the authenticated user (JWT "sub" claim).
	UserID string

	// TokenID is the unique JWT ID (JWT "jti" claim).
	// Used to look up the token in the deny list for revocation checks.
	// Every issued token must have a unique JTI — the mock issuer
	// generates ULIDs for this purpose.
	TokenID string

	// WorkspaceID is the ULID of the workspace this token grants access to.
	// This is a custom claim ("workspace_id") set by the token issuer.
	// All resource operations are scoped to this workspace.
	WorkspaceID string

	// Scope is the space-separated list of permissions granted to this token.
	// Standard scopes: "read", "write", "admin"
	// The authorization layer (Story 2.2) validates scope per endpoint.
	Scope string

	// Issuer is the "iss" claim — the identity of the token issuer.
	// Validated against the configured JWT_ISSUER value.
	Issuer string

	// Audiences is the "aud" claim — the intended recipients of the token.
	// The middleware validates that JWT_AUDIENCE is included.
	Audiences []string

	// IssuedAt is when the token was created ("iat" claim).
	IssuedAt time.Time

	// ExpiresAt is when the token expires ("exp" claim).
	// The middleware validates this is in the future.
	ExpiresAt time.Time
}

// HasScope reports whether the claims include the given permission scope.
// Scope comparison is case-sensitive and exact-word-matched —
// "write" does not match "writeonly" or "WRITE".
func (c *Claims) HasScope(scope string) bool {
	if c == nil {
		return false
	}
	// Parse space-separated scope string
	start := 0
	s := c.Scope
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ' ' {
			if s[start:i] == scope {
				return true
			}
			start = i + 1
		}
	}
	return false
}

// IsExpired reports whether the token has passed its expiry time.
func (c *Claims) IsExpired() bool {
	if c == nil {
		return true
	}
	return time.Now().UTC().After(c.ExpiresAt)
}

// WithContext stores Claims in a context.Context.
// Called by the JWT middleware after successful validation.
// The value is intentionally stored under an unexported key so that
// only this package can read it — preventing claims spoofing by
// setting the key from outside this package.
func WithContext(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, claimsContextKey, claims)
}

// FromContext retrieves Claims from a context.
// Returns (nil, false) if no claims are present — this means the
// request was not authenticated (the middleware did not run or
// validation failed before the handler was reached).
//
// Usage in handlers:
//
//	claims, ok := auth.FromContext(r.Context())
//	if !ok {
//	    // Should not happen if auth middleware is applied — but defensive check
//	    response.WriteProblem(w, response.Problem{Status: 401, ...})
//	    return
//	}
func FromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsContextKey).(*Claims)
	if !ok || c == nil {
		return nil, false
	}
	return c, true
}

// WorkspaceIDFromContext extracts the workspace ID from the request context.
// Returns an empty string if claims are not present.
// Convenience wrapper used by HTTP handlers.
func WorkspaceIDFromContext(ctx context.Context) string {
	c, ok := FromContext(ctx)
	if !ok {
		return ""
	}
	return c.WorkspaceID
}

// UserIDFromContext extracts the user ID from the request context.
// Returns an empty string if claims are not present.
func UserIDFromContext(ctx context.Context) string {
	c, ok := FromContext(ctx)
	if !ok {
		return ""
	}
	return c.UserID
}
