// Package ratelimit defines the domain model for rate limiting policy.
//
// Rate limiting sits at the boundary between the domain and infrastructure
// layers. The policy (what limits apply to whom) is a business rule — it
// belongs in the domain. The enforcement mechanism (Redis token bucket Lua
// script) is infrastructure — it belongs in the infrastructure layer.
//
// PRD section 10.3 defines the rate limit tiers:
//
//	Actor                    Limit        Window
//	─────────────────────────────────────────────
//	Unauthenticated (by IP)  10 req       60s
//	Free tier (by API key)   100 req      60s
//	Pro tier (by API key)    1,000 req    60s
//	Enterprise (by API key)  configurable configurable
//	Redirect (by IP)         300 req      60s
//
// Algorithm: Token Bucket
//
//	Chosen over fixed-window (too bursty at window edges) and
//	sliding-window log (too memory-intensive at scale).
//	Token bucket allows controlled bursts up to the bucket capacity
//	while enforcing a sustained average rate. It is the same algorithm
//	used by AWS API Gateway, Stripe, and Cloudflare.
//
// Key insight: the bucket capacity equals the limit, and the refill rate
// is limit/window. A "free" client gets 100 tokens at start, consumes
// them, and gets 100 new tokens after 60 seconds. A burst of 100 requests
// in 1 second is allowed (consumes the whole bucket), but then the client
// must wait for tokens to refill before making more requests.
package ratelimit

import (
	"fmt"
	"strings"
	"time"
)

// Tier represents a client's subscription tier.
// Determines which rate limit policy applies.
type Tier string

const (
	TierUnauthenticated Tier = "unauthenticated"
	TierFree            Tier = "free"
	TierPro             Tier = "pro"
	TierEnterprise      Tier = "enterprise"
)

// EndpointClass groups endpoints with similar traffic characteristics.
// Different classes have different rate limit policies even for the same tier.
//
// Redirect is separated from Write because:
//   - Redirects are read-only and cacheable — high volume is expected
//   - Write operations create DB records — lower volume is appropriate
//   - Allowing high redirect rates while limiting writes protects the DB
type EndpointClass string

const (
	// ClassRedirect covers GET /{shortcode} — the highest-volume endpoint.
	ClassRedirect EndpointClass = "redirect"

	// ClassRead covers GET /api/v1/urls, GET /api/v1/workspaces, etc.
	ClassRead EndpointClass = "read"

	// ClassWrite covers POST/PATCH/DELETE operations that mutate state.
	ClassWrite EndpointClass = "write"
)

// Policy defines the rate limit parameters for one (Tier, EndpointClass) pair.
type Policy struct {
	// Limit is the maximum number of requests allowed per Window.
	// Also the initial token bucket capacity.
	Limit int

	// Window is the duration over which Limit requests are allowed.
	// After Window elapses, the bucket refills to Limit tokens.
	Window time.Duration

	// BurstFactor controls how many tokens can accumulate beyond
	// the base limit. BurstFactor=1 means no burst beyond Limit.
	// BurstFactor=2 means clients can accumulate up to 2×Limit tokens
	// after idle periods.
	//
	// For most tiers: BurstFactor=1 (strict).
	// For redirect: BurstFactor=2 (viral content may legitimately spike).
	BurstFactor int
}

// BucketCapacity returns the maximum number of tokens the bucket can hold.
// A burst factor of 1 means no burst above the base limit.
func (p Policy) BucketCapacity() int {
	if p.BurstFactor <= 1 {
		return p.Limit
	}
	return p.Limit * p.BurstFactor
}

// RefillRatePerSecond returns how many tokens are added per second.
// Used by the Lua token bucket script to calculate token accumulation.
func (p Policy) RefillRatePerSecond() float64 {
	return float64(p.Limit) / p.Window.Seconds()
}

// defaultPolicies is the authoritative rate limit policy matrix.
// Tier × EndpointClass → Policy
// This is the single source of truth — used by the middleware and
// available for testing without any infrastructure dependency.
var defaultPolicies = map[Tier]map[EndpointClass]Policy{
	TierUnauthenticated: {
		ClassRedirect: {Limit: 300, Window: 60 * time.Second, BurstFactor: 2},
		ClassRead:     {Limit: 10, Window: 60 * time.Second, BurstFactor: 1},
		ClassWrite:    {Limit: 10, Window: 60 * time.Second, BurstFactor: 1},
	},
	TierFree: {
		ClassRedirect: {Limit: 1000, Window: 60 * time.Second, BurstFactor: 2},
		ClassRead:     {Limit: 100, Window: 60 * time.Second, BurstFactor: 1},
		ClassWrite:    {Limit: 100, Window: 60 * time.Second, BurstFactor: 1},
	},
	TierPro: {
		ClassRedirect: {Limit: 10000, Window: 60 * time.Second, BurstFactor: 2},
		ClassRead:     {Limit: 1000, Window: 60 * time.Second, BurstFactor: 1},
		ClassWrite:    {Limit: 1000, Window: 60 * time.Second, BurstFactor: 1},
	},
	TierEnterprise: {
		ClassRedirect: {Limit: 100000, Window: 60 * time.Second, BurstFactor: 3},
		ClassRead:     {Limit: 10000, Window: 60 * time.Second, BurstFactor: 2},
		ClassWrite:    {Limit: 10000, Window: 60 * time.Second, BurstFactor: 2},
	},
}

// GetPolicy returns the rate limit policy for a given tier and endpoint class.
// Falls back to the unauthenticated policy if the tier is unknown.
func GetPolicy(tier Tier, class EndpointClass) Policy {
	if tierPolicies, ok := defaultPolicies[tier]; ok {
		if policy, ok := tierPolicies[class]; ok {
			return policy
		}
	}
	// Safe fallback — always return a policy, never panic.
	return defaultPolicies[TierUnauthenticated][ClassWrite]
}

// Result is the outcome of a rate limit check.
type Result struct {
	// Allowed is true if the request should be served.
	Allowed bool

	// Remaining is the number of tokens left in the bucket after this request.
	Remaining int

	// Limit is the total bucket capacity.
	Limit int

	// ResetAt is when the bucket will be refilled to capacity.
	ResetAt time.Time

	// RetryAfter is how long the client should wait before retrying.
	// Only meaningful when Allowed=false.
	RetryAfter time.Duration
}

// Headers returns the standard rate-limit HTTP response headers.
// We follow the IETF draft-ietf-httpapi-ratelimit-headers standard:
//
//	RateLimit-Limit:     bucket capacity
//	RateLimit-Remaining: tokens left after this request
//	RateLimit-Reset:     Unix timestamp when bucket refills
//	Retry-After:         seconds to wait (only when 429)
func (r *Result) Headers() map[string]string {
	h := map[string]string{
		"RateLimit-Limit":     fmt.Sprintf("%d", r.Limit),
		"RateLimit-Remaining": fmt.Sprintf("%d", r.Remaining),
		"RateLimit-Reset":     fmt.Sprintf("%d", r.ResetAt.Unix()),
	}
	if !r.Allowed {
		h["Retry-After"] = fmt.Sprintf("%.0f", r.RetryAfter.Seconds())
	}
	return h
}

// IdentityKey builds a Redis key for rate limiting based on the caller identity.
//
// Key format:  rl:{service}:{tier}:{class}:{identity}
// Examples:
//
//	rl:api:free:write:usr_01HXYZ           (JWT user)
//	rl:api:free:write:key_01HABC           (API key ID)
//	rl:redirect:unauthenticated:redirect:192.168.1.1  (IP)
//
// Using a structured key prefix allows:
//   - Bulk invalidation by tier: SCAN rl:api:free:*
//   - Monitoring by class: count keys matching rl:*:write:*
//   - Isolation between services
func IdentityKey(service string, tier Tier, class EndpointClass, identity string) string {
	// Sanitise identity to prevent key injection.
	// Replace any colon in identity (e.g. IPv6 addresses) with underscore.
	safeIdentity := strings.ReplaceAll(identity, ":", "_")
	return fmt.Sprintf("rl:%s:%s:%s:%s", service, tier, class, safeIdentity)
}
