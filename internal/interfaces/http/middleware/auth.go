package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/v2/jwk"
	"github.com/lestrrat-go/jwx/v2/jws"
	"github.com/lestrrat-go/jwx/v2/jwt"

	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// DenyListChecker is the interface for token revocation checks.
// Defined here (at the consumer boundary) not in the infrastructure layer.
type DenyListChecker interface {
	IsRevoked(ctx context.Context, jti string) (bool, error)
}

// AuthConfig holds configuration for the JWT authentication middleware.
type AuthConfig struct {
	// Issuer is the expected "iss" claim value.
	// Tokens with a different issuer are rejected.
	// Must match JWT_ISSUER in .env and the issuer in the mock issuer.
	Issuer string

	// Audience is the expected "aud" claim value.
	// Tokens must include this audience in their "aud" array.
	// Must match JWT_AUDIENCE in .env.
	Audience string

	// KeySet is the JWK Set containing the public key(s) for signature verification.
	// Loaded from JWT_PUBLIC_KEY_PATH at startup.
	// In Phase 4, fetched from WSO2's JWKS endpoint.
	KeySet jwk.Set

	// DenyList is the token revocation checker.
	// May be nil — if so, deny list checks are skipped with a warning.
	// Nil is used in tests and when Redis is unavailable.
	DenyList DenyListChecker

	// Log is the service logger.
	Log *slog.Logger
}

// Authenticate returns a chi-compatible JWT authentication middleware.
//
// Validation sequence for every request:
//  1. Extract Bearer token from Authorization header
//  2. Parse and cryptographically verify RS256 signature against public key
//  3. Validate standard claims: exp (not expired), iss (correct issuer), aud (correct audience)
//  4. Extract custom claim: workspace_id (required)
//  5. Extract jti claim and check Redis deny list (token not revoked)
//  6. Store verified Claims in request context
//  7. Call next handler
//
// On any failure: return 401 Unauthorized with RFC 7807 Problem Details.
// The error detail is intentionally vague ("invalid or expired token")
// to avoid leaking information about which specific check failed —
// an attacker could use that information to craft better attacks.
//
// Routes that must bypass auth (health probes):
//
//	Apply this middleware to the /api/v1 sub-router only:
//	r.Route("/api/v1", func(r chi.Router) {
//	    r.Use(middleware.Authenticate(cfg))
//	    ...
//	})
//	/healthz and /readyz are registered on the root router and
//	are never wrapped by this middleware.
func Authenticate(cfg AuthConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := domainauth.FromContext(r.Context()); ok {
				next.ServeHTTP(w, r)
				return
			}

			log := logger.FromContext(r.Context())

			// Step 1: Extract Bearer token
			token, err := extractBearerToken(r)
			if err != nil {
				writeAuthError(w, r, domainauth.ErrMissingToken, log)
				return
			}

			// Step 2 & 3: Parse + verify signature + validate standard claims.
			// jwt.Parse does all of this in one call:
			//   - Verifies RS256 signature against the provided key set
			//   - Validates exp (must be in the future)
			//   - Validates iss (must match WithIssuer)
			//   - Validates aud (must include WithAudience)
			parsed, err := jwt.Parse(
				[]byte(token),
				jwt.WithKeySet(
					cfg.KeySet,
					jws.WithRequireKid(false),
					jws.WithInferAlgorithmFromKey(true),
					jws.WithUseDefault(true),
				),
				jwt.WithValidate(true),
				jwt.WithIssuer(cfg.Issuer),
				jwt.WithAudience(cfg.Audience),
				jwt.WithAcceptableSkew(30*time.Second), // Allow 30s clock skew
			)
			if err != nil {
				log.Debug("jwt parse/validation failed",
					slog.String("error", err.Error()),
				)
				// Distinguish expired from invalid for logging purposes
				// (both return 401 to the client — do not leak this distinction).
				if isExpiredError(err) {
					writeAuthError(w, r, domainauth.ErrTokenExpired, log)
				} else {
					writeAuthError(w, r, domainauth.ErrInvalidToken, log)
				}
				return
			}

			// Step 4: Extract required custom claim workspace_id.
			workspaceID, ok := parsed.PrivateClaims()["workspace_id"].(string)
			if !ok || workspaceID == "" {
				log.Warn("token missing workspace_id claim",
					slog.String("sub", parsed.Subject()),
				)
				writeAuthError(w, r, domainauth.ErrMissingClaim, log)
				return
			}

			// Extract jti — required for deny list check.
			jti := parsed.JwtID()
			if jti == "" {
				log.Warn("token missing jti claim",
					slog.String("sub", parsed.Subject()),
				)
				writeAuthError(w, r, domainauth.ErrMissingClaim, log)
				return
			}

			// Step 5: Deny list check.
			// Skip if DenyList is nil (fail-open for development/test scenarios).
			if cfg.DenyList != nil {
				revoked, err := cfg.DenyList.IsRevoked(r.Context(), jti)
				if err != nil {
					// Redis unavailable — fail open with a warning.
					// See denylist.go for the security trade-off rationale.
					log.Warn("deny list check failed, failing open",
						slog.String("error", err.Error()),
						slog.String("jti_prefix", jti[:min(8, len(jti))]),
					)
				} else if revoked {
					writeAuthError(w, r, domainauth.ErrTokenRevoked, log)
					return
				}
			}

			// Step 6: Build and store Claims in context.
			scope, _ := parsed.PrivateClaims()["scope"].(string)

			claims := &domainauth.Claims{
				UserID:      parsed.Subject(),
				TokenID:     jti,
				WorkspaceID: workspaceID,
				Scope:       scope,
				Issuer:      parsed.Issuer(),
				Audiences:   parsed.Audience(),
				IssuedAt:    parsed.IssuedAt(),
				ExpiresAt:   parsed.Expiration(),
			}

			ctx := domainauth.WithContext(r.Context(), claims)

			// Enrich the request-scoped logger with identity fields.
			// All subsequent log lines from handlers automatically include
			// user_id and workspace_id — zero extra work in handlers.
			enrichedLog := logger.WithUserContext(
				logger.FromContext(ctx),
				claims.UserID,
				claims.WorkspaceID,
			)
			ctx = logger.WithContext(ctx, enrichedLog)

			// Step 7: Pass enriched context to the next handler.
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireScope returns a middleware that enforces a specific scope on top
// of authentication. Apply after Authenticate in the middleware chain.
//
// Usage:
//
//	r.With(middleware.RequireScope("write")).Post("/urls", handler.Handle)
//
// This is the coarse-grained scope check at the HTTP layer.
// Fine-grained resource authorization (workspace ownership) happens
// in the application use cases (Story 2.2).
func RequireScope(scope string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := domainauth.FromContext(r.Context())
			if !ok || !claims.HasScope(scope) {
				response.WriteProblem(w, response.Problem{
					Type:     response.ProblemTypeUnauthorized,
					Title:    "Forbidden",
					Status:   http.StatusForbidden,
					Detail:   "Your token does not have the required scope: " + scope,
					Instance: r.URL.Path,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// extractBearerToken extracts the token string from the Authorization header.
// Expected format: "Authorization: Bearer <token>"
// Returns ErrMissingToken if the header is absent or malformed.
func extractBearerToken(r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return "", domainauth.ErrMissingToken
	}

	// strings.Cut is more efficient than strings.SplitN for this pattern.
	scheme, token, found := strings.Cut(authHeader, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", domainauth.ErrMissingToken
	}

	return token, nil
}

// writeAuthError writes a 401 RFC 7807 Problem Details response.
// The detail message is deliberately generic — do not include the
// specific error reason (which check failed) in the response body.
func writeAuthError(w http.ResponseWriter, r *http.Request, err error, log *slog.Logger) {
	log.Debug("authentication failed", slog.String("reason", err.Error()))

	response.WriteProblem(w, response.Problem{
		Type:     response.ProblemTypeUnauthenticated,
		Title:    "Unauthorized",
		Status:   http.StatusUnauthorized,
		Detail:   "Your request could not be authenticated. Provide a valid Bearer token.",
		Instance: r.URL.Path,
	})
}

// isExpiredError returns true if the jwx error indicates token expiry.
// Used for internal logging differentiation only — both expired and
// invalid tokens return 401 to the client.
func isExpiredError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exp not satisfied")
}

// min returns the smaller of two ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
