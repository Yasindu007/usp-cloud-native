package middleware

import (
	"context"
	"log/slog"
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"

	domainauth "github.com/urlshortener/platform/internal/domain/auth"
	"github.com/urlshortener/platform/internal/domain/ratelimit"
	"github.com/urlshortener/platform/internal/infrastructure/metrics"
	"github.com/urlshortener/platform/internal/interfaces/http/response"
	"github.com/urlshortener/platform/pkg/logger"
)

// Limiter is the interface for performing rate limit checks.
// Implemented by redis.TokenBucketLimiter.
// Defined at the middleware boundary (consumer) — not in infrastructure.
type Limiter interface {
	Check(ctx context.Context, key string, policy ratelimit.Policy) (*ratelimit.Result, error)
}

// RateLimitConfig configures the rate limiting middleware.
type RateLimitConfig struct {
	// Limiter performs the actual token bucket check against Redis.
	Limiter Limiter

	// ServiceName is used in the rate limit Redis key to namespace
	// limits per service (api-service vs redirect-service).
	ServiceName string

	Metrics *metrics.Metrics

	// EndpointClass classifies this router group (redirect, read, write).
	// Applied uniformly to all routes the middleware wraps.
	// For fine-grained per-route control, apply middleware per route.
	EndpointClass ratelimit.EndpointClass

	// Log is the service logger.
	Log *slog.Logger

	// FailOpen controls behaviour when Redis is unavailable.
	// true  = allow requests when Redis is down (default)
	//         → protects availability SLO at cost of rate limit enforcement
	// false = deny requests when Redis is down (strict)
	//         → protects backend at cost of availability
	FailOpen bool
}

// RateLimit returns a chi-compatible middleware that enforces rate limits
// using the token bucket algorithm backed by Redis.
//
// Identity resolution priority (highest to lowest):
//  1. API key ID        (most specific — per-key control)
//  2. JWT subject (sub) (per-user control)
//  3. Client IP         (fallback for unauthenticated requests)
//
// Tier resolution:
//
//	Claims present → TierFree (Phase 2 default; Phase 3 adds plan lookup)
//	No claims      → TierUnauthenticated
//
// Header behaviour:
//
//	Every response receives RateLimit-* headers regardless of allow/deny.
//	These are standard (IETF draft-ietf-httpapi-ratelimit-headers) and
//	allow clients to implement proactive back-off without hitting 429s.
//
// Fail-open when Redis is unavailable:
//
//	Default behaviour is fail-open (allow requests).
//	See RateLimitConfig.FailOpen for rationale.
//
// Position in middleware chain (correct order):
//
//	RequestID → RealIP → OTel → Logger → Metrics → APIKeyAuth → Authenticate
//	→ WorkspaceAuth → RateLimit → RequireAction → Handler
//
//	Rate limiting must run AFTER authentication because the identity key
//	depends on who is authenticated. Before auth, we only have the IP.
func RateLimit(cfg RateLimitConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			log := logger.FromContext(r.Context())

			// ── Resolve identity and tier ─────────────────────────────────────
			identity, tier := resolveIdentity(r)

			// ── Build rate limit key ──────────────────────────────────────────
			key := ratelimit.IdentityKey(cfg.ServiceName, tier, cfg.EndpointClass, identity)

			// ── Get policy for this tier/class ────────────────────────────────
			policy := ratelimit.GetPolicy(tier, cfg.EndpointClass)

			// ── Perform token bucket check ────────────────────────────────────
			result, err := cfg.Limiter.Check(r.Context(), key, policy)
			if err != nil {
				// Infrastructure failure — apply fail-open/fail-closed policy.
				log.Warn("rate limit check failed",
					slog.String("error", err.Error()),
					slog.Bool("fail_open", cfg.FailOpen),
					slog.String("identity", truncate(identity, 16)),
				)

				if !cfg.FailOpen {
					response.WriteProblem(w, response.Problem{
						Type:     response.ProblemTypeInternal,
						Title:    "Service Unavailable",
						Status:   http.StatusServiceUnavailable,
						Detail:   "Rate limiting service is temporarily unavailable.",
						Instance: r.URL.Path,
					})
					return
				}
				// Fail open: let request through without rate limit headers.
				next.ServeHTTP(w, r)
				return
			}

			// ── Write standard rate limit headers ─────────────────────────────
			// Headers are written on EVERY response — not just 429s.
			// This allows API clients to implement proactive back-off.
			for k, v := range result.Headers() {
				w.Header().Set(k, v)
			}

			// ── Block if over limit ───────────────────────────────────────────
			resultLabel := "allowed"
			if !result.Allowed {
				resultLabel = "denied"
			}
			if cfg.Metrics != nil {
				cfg.Metrics.RecordRateLimit(
					cfg.ServiceName,
					string(tier),
					string(cfg.EndpointClass),
					resultLabel,
				)
			}

			if !result.Allowed {
				log.Debug("request rate limited",
					slog.String("identity", truncate(identity, 16)),
					slog.String("tier", string(tier)),
					slog.String("class", string(cfg.EndpointClass)),
					slog.String("request_id", chimiddleware.GetReqID(r.Context())),
				)

				response.WriteProblem(w, response.Problem{
					Type:   "https://docs.shortener.example.com/errors/rate-limit-exceeded",
					Title:  "Too Many Requests",
					Status: http.StatusTooManyRequests,
					Detail: "You have exceeded the rate limit. Check the RateLimit-Reset header " +
						"to see when your quota resets, or the Retry-After header for how long to wait.",
					Instance: r.URL.Path,
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// resolveIdentity returns the rate limit identity string and tier for a request.
//
// The identity string is the key that uniquely identifies the rate limit bucket.
// We use the most specific identifier available:
//   - API key ID: per-key isolation (different keys in same workspace have
//     independent buckets — a script using one key doesn't affect another)
//   - JWT sub: per-user isolation (one user per workspace bucket)
//   - IP: per-IP isolation (unauthenticated)
//
// Tier is currently fixed to Free for all authenticated users (Phase 2).
// Phase 3 adds workspace plan lookup (free/pro/enterprise) via the workspace
// repository, enabling true per-tier rate limiting based on subscription.
func resolveIdentity(r *http.Request) (identity string, tier ratelimit.Tier) {
	claims, ok := domainauth.FromContext(r.Context())
	if !ok {
		// Unauthenticated: rate limit by IP address.
		// r.RemoteAddr is already resolved by chi's RealIP middleware
		// (reads X-Real-IP or X-Forwarded-For).
		return r.RemoteAddr, ratelimit.TierUnauthenticated
	}

	// Authenticated via API key: the TokenID field is the API key ID
	// when claims were injected by APIKeyAuth middleware.
	// For JWT auth: TokenID is the JTI (per-token isolation).
	// Using TokenID (not UserID) gives us per-credential isolation —
	// a user can have multiple API keys with independent rate limits.
	if claims.TokenID != "" {
		return claims.TokenID, ratelimit.TierFree
	}

	// Fallback: use user ID (should not normally reach here).
	return claims.UserID, ratelimit.TierFree
}

// truncate returns the first n chars of s for safe logging.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
