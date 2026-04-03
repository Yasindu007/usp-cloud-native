package auth

import "errors"

// Authentication domain errors.
// The HTTP middleware translates these to RFC 7807 Problem Details responses.
//
// Error mapping:
//   ErrMissingToken    → 401 Unauthorized (no Authorization header)
//   ErrInvalidToken    → 401 Unauthorized (malformed, bad signature)
//   ErrTokenExpired    → 401 Unauthorized (exp claim in the past)
//   ErrTokenRevoked    → 401 Unauthorized (JTI in deny list)
//   ErrInvalidIssuer   → 401 Unauthorized (iss claim mismatch)
//   ErrInvalidAudience → 401 Unauthorized (aud claim mismatch)
//   ErrInsufficientScope → 403 Forbidden (valid token, wrong scope)
//
// We return 401 for all token validity failures — not 403. This is
// intentional: 403 means "authenticated but not authorized." Token
// failures mean we cannot establish identity at all, which is 401.
var (
	ErrMissingToken      = errors.New("auth: Authorization header is missing or not Bearer scheme")
	ErrInvalidToken      = errors.New("auth: token is malformed or has invalid signature")
	ErrTokenExpired      = errors.New("auth: token has expired")
	ErrTokenRevoked      = errors.New("auth: token has been revoked")
	ErrInvalidIssuer     = errors.New("auth: token issuer does not match expected value")
	ErrInvalidAudience   = errors.New("auth: token audience does not match expected value")
	ErrMissingClaim      = errors.New("auth: required claim is missing from token")
	ErrInsufficientScope = errors.New("auth: token does not have the required scope")
)
