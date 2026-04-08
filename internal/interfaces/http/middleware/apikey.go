package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	domainapikey "github.com/urlshortener/platform/internal/domain/apikey"
	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/keyutil"
	"github.com/urlshortener/platform/pkg/logger"
)

// APIKeyLookup is the interface for retrieving API key candidates by prefix.
// Implemented by postgres.APIKeyRepository.
type APIKeyLookup interface {
	GetByPrefix(ctx context.Context, prefix string) ([]*domainapikey.APIKey, error)
	UpdateLastUsed(ctx context.Context, id string) error
}

// APIKeyAuth returns middleware that authenticates requests using API keys.
//
// API keys are accepted in two header formats:
//  1. Authorization: Bearer urlsk_...   (same as JWT, machine-friendly)
//  2. X-API-Key: urlsk_...             (explicit API key header)
//
// Format 1 is checked first. If the Authorization header contains a JWT
// (does not start with "urlsk_"), this middleware returns immediately
// without modifying the response, allowing the JWT middleware to handle it.
//
// This enables the dual-authentication pattern in the router:
//
//	r.Use(middleware.APIKeyAuth(lookup, log))
//	r.Use(middleware.Authenticate(jwtCfg))
//
// Either authenticates the request — if neither does, the handler returns 401.
//
// Authentication flow:
//  1. Extract key from header
//  2. Extract 14-char prefix from key
//  3. Fetch all active keys with that prefix from DB (usually 1 row)
//  4. bcrypt.Compare(sha256(submitted), stored_hash) for each candidate
//  5. On match: build synthetic Claims and store in context
//  6. Async: update last_used_at (never blocks the response)
//
// Timing attack note:
//
//	bcrypt is inherently timing-safe for the hash comparison itself.
//	The prefix lookup leaks timing information about how many keys share
//	a prefix (0 vs 1+ candidates). This is acceptable because:
//	- Prefixes are 14 chars of high-entropy random data (attacker cannot
//	  predict which prefixes map to real keys)
//	- The timing difference is ~0.1ms (DB round-trip) vs ~250ms (bcrypt)
//	  — the signal is buried in bcrypt's dominant timing.
func APIKeyAuth(lookup APIKeyLookup, log *slog.Logger) func(http.Handler) http.Handler {
	tracer := otel.Tracer("github.com/urlshortener/platform/internal/interfaces/http/middleware")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Extract raw key from headers.
			rawKey, ok := extractAPIKey(r)
			if !ok {
				// No API key found — pass through to JWT middleware.
				next.ServeHTTP(w, r)
				return
			}

			ctx, span := tracer.Start(r.Context(), "APIKeyAuth",
				trace.WithAttributes(
					attribute.String("apikey.prefix", safePrefix(rawKey)),
				),
			)
			defer span.End()

			log := logger.FromContext(ctx)

			// Extract prefix for DB lookup.
			prefix := domainapikey.ExtractPrefix(rawKey)
			if prefix == "" {
				// Key format is wrong (too short or wrong prefix).
				log.Debug("api key has invalid format")
				writeAPIKeyError(w, r)
				return
			}

			// Fetch candidates from DB.
			candidates, err := lookup.GetByPrefix(ctx, prefix)
			if err != nil {
				log.Warn("api key prefix lookup failed",
					slog.String("error", err.Error()),
				)
				response.InternalError(w, r.URL.Path)
				return
			}

			// bcrypt comparison for each candidate.
			// In practice: almost always exactly 1 candidate.
			var matched *domainapikey.APIKey
			for _, candidate := range candidates {
				if keyutil.Verify(rawKey, candidate.KeyHash) {
					matched = candidate
					break
				}
			}

			if matched == nil {
				log.Debug("api key verification failed")
				writeAPIKeyError(w, r)
				return
			}

			// Async: update last_used_at without blocking the response.
			// Uses a detached context — the request context will be cancelled
			// before the goroutine completes.
			go func() {
				bgCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := lookup.UpdateLastUsed(bgCtx, matched.ID); err != nil {
					log.Warn("failed to update api key last_used_at",
						slog.String("key_id", matched.ID),
						slog.String("error", err.Error()),
					)
				}
			}()

			// Build synthetic Claims from the API key.
			// This makes API key authentication transparent to handlers —
			// they read from domainauth.FromContext() regardless of whether
			// a JWT or an API key was used.
			claims := &domainauth.Claims{
				UserID:      matched.CreatedBy, // key creator as the "user"
				TokenID:     matched.ID,        // key ID for audit logging
				WorkspaceID: matched.WorkspaceID,
				Scope:       matched.ScopeString(), // "read write" etc.
				Issuer:      "apikey",
				IssuedAt:    matched.CreatedAt,
				ExpiresAt:   expiresAtOrFar(matched.ExpiresAt),
			}

			authCtx := domainauth.WithContext(ctx, claims)
			enrichedLog := logger.WithUserContext(
				logger.FromContext(authCtx),
				claims.UserID,
				claims.WorkspaceID,
			)
			authCtx = logger.WithContext(authCtx, enrichedLog)

			log.Debug("api key authenticated",
				slog.String("key_id", matched.ID),
				slog.String("workspace_id", matched.WorkspaceID),
			)

			span.SetAttributes(
				attribute.String("apikey.id", matched.ID),
				attribute.String("workspace.id", matched.WorkspaceID),
			)

			next.ServeHTTP(w, r.WithContext(authCtx))
		})
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

// extractAPIKey extracts a raw API key from the request.
// Checks X-API-Key first, then Authorization: Bearer urlsk_...
// Returns (key, true) if found, ("", false) if not present.
func extractAPIKey(r *http.Request) (string, bool) {
	// X-API-Key header (explicit)
	if v := r.Header.Get("X-API-Key"); v != "" {
		if strings.HasPrefix(v, domainapikey.KeyPrefix) {
			return v, true
		}
	}

	// Authorization: Bearer urlsk_... header
	if auth := r.Header.Get("Authorization"); auth != "" {
		scheme, token, found := strings.Cut(auth, " ")
		if found && strings.EqualFold(scheme, "Bearer") &&
			strings.HasPrefix(token, domainapikey.KeyPrefix) {
			return token, true
		}
	}

	return "", false
}

// writeAPIKeyError returns 401 for invalid API key credentials.
func writeAPIKeyError(w http.ResponseWriter, r *http.Request) {
	response.WriteProblem(w, response.Problem{
		Type:     response.ProblemTypeUnauthenticated,
		Title:    "Unauthorized",
		Status:   http.StatusUnauthorized,
		Detail:   "Your request could not be authenticated. Provide a valid API key.",
		Instance: r.URL.Path,
	})
}

// safePrefix returns the first 14 chars of a key for logging.
// Never logs the full key.
func safePrefix(key string) string {
	if len(key) >= domainapikey.RawKeyPrefixLength {
		return key[:domainapikey.RawKeyPrefixLength]
	}
	return key
}

// expiresAtOrFar returns the expiry time or a far-future time for
// keys with no expiry, so Claims.IsExpired() works correctly.
func expiresAtOrFar(t *time.Time) time.Time {
	if t == nil {
		return time.Now().Add(100 * 365 * 24 * time.Hour) // ~100 years
	}
	return *t
}
