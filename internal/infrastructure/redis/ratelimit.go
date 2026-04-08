package redis

import (
	"context"
	"fmt"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/urlshortener/platform/internal/domain/ratelimit"
)

const ratelimitTracerName = "github.com/urlshortener/platform/internal/infrastructure/redis/ratelimit"

// TokenBucketLimiter implements a distributed token bucket rate limiter
// using Redis and a Lua script for atomic execution.
//
// Why Lua?
//
//	A token bucket requires multiple Redis operations:
//	  1. GET current token count and last refill time
//	  2. Calculate new token count based on elapsed time
//	  3. Decrement by 1 (consume one token)
//	  4. SET updated count and timestamp
//
//	If these run as separate commands, a race condition exists:
//	between GET and SET, another request on a different pod could
//	read the same count, causing both requests to be allowed when
//	only one token remains.
//
//	Lua scripts execute atomically on the Redis server — no other
//	command can interrupt them. This gives us the same consistency
//	guarantee as a database transaction, without distributed locks.
//
// Script logic:
//
//	The script receives: key, capacity, refill_rate, now_unix_ms, cost
//	It returns: [allowed(0/1), remaining, reset_at_unix_ms]
//
//	1. Load current state: {tokens, last_refill_ms} or initialise
//	2. Calculate elapsed = now - last_refill
//	3. Add elapsed * refill_rate new tokens (capped at capacity)
//	4. If tokens >= cost: subtract cost, allowed=1
//	   Else: allowed=0, remaining=0
//	5. Save updated state with TTL = 2 × window
//	6. Return [allowed, floor(tokens), reset_ms]
//
// The TTL (2 × window) ensures Redis automatically cleans up inactive
// client buckets — important for IP-based keys which could number in millions.
var tokenBucketScript = goredis.NewScript(`
local key           = KEYS[1]
local capacity      = tonumber(ARGV[1])
local refill_rate   = tonumber(ARGV[2])  -- tokens per millisecond
local now_ms        = tonumber(ARGV[3])
local cost          = tonumber(ARGV[4])
local ttl_ms        = tonumber(ARGV[5])

-- Load existing bucket state.
-- Format: "tokens:last_refill_ms"
local data = redis.call('GET', key)

local tokens
local last_refill_ms

if data then
	local sep = string.find(data, ':')
	tokens        = tonumber(string.sub(data, 1, sep - 1))
	last_refill_ms = tonumber(string.sub(data, sep + 1))
else
	-- First request from this client: start with a full bucket.
	tokens        = capacity
	last_refill_ms = now_ms
end

-- Refill: add tokens based on elapsed time since last refill.
local elapsed_ms = math.max(0, now_ms - last_refill_ms)
local new_tokens = elapsed_ms * refill_rate
tokens = math.min(capacity, tokens + new_tokens)
last_refill_ms = now_ms

-- Attempt to consume 'cost' tokens.
local allowed
local remaining
if tokens >= cost then
	tokens    = tokens - cost
	allowed   = 1
	remaining = math.floor(tokens)
else
	allowed   = 0
	remaining = 0
end

-- Persist updated bucket state.
-- TTL prevents unbounded key accumulation in Redis.
local state = tostring(tokens) .. ':' .. tostring(last_refill_ms)
local ttl_s = math.ceil(ttl_ms / 1000)
redis.call('SET', key, state, 'EX', ttl_s)

-- Calculate when the bucket will next be full (for Reset header).
local tokens_needed = capacity - tokens
local ms_to_full
if refill_rate > 0 then
	ms_to_full = math.ceil(tokens_needed / refill_rate)
else
	ms_to_full = ttl_ms
end
local reset_ms = now_ms + ms_to_full

return {allowed, remaining, reset_ms}
`)

// TokenBucketLimiter is the Redis-backed token bucket rate limiter.
type TokenBucketLimiter struct {
	client *Client
}

// NewTokenBucketLimiter creates a TokenBucketLimiter backed by the given client.
func NewTokenBucketLimiter(client *Client) *TokenBucketLimiter {
	return &TokenBucketLimiter{client: client}
}

// Check performs a rate limit check for a single request (cost=1).
//
// Parameters:
//
//	ctx      — request context (for cancellation and tracing)
//	key      — unique identifier for this rate limit bucket
//	           (built by ratelimit.IdentityKey)
//	policy   — the rate limit parameters to enforce
//
// Return semantics:
//
//	(result, nil)  — check completed; result.Allowed indicates permit/deny
//	(nil, err)     — Redis unavailable; caller decides fail-open vs fail-closed
//
// Latency: ~0.5–2ms (one Redis round-trip for the Lua script evaluation).
// The script is loaded once on first use and cached by its SHA1 on the
// Redis server — subsequent calls use EVALSHA (faster than EVAL).
func (l *TokenBucketLimiter) Check(
	ctx context.Context,
	key string,
	policy ratelimit.Policy,
) (*ratelimit.Result, error) {
	_, span := otel.Tracer(ratelimitTracerName).Start(ctx, "TokenBucketLimiter.Check",
		trace.WithAttributes(
			attribute.String("rl.key_prefix", safeKeyPrefix(key)),
			attribute.Int("rl.limit", policy.Limit),
		),
	)
	defer span.End()

	now := time.Now()
	nowMS := now.UnixMilli()

	capacity := policy.BucketCapacity()
	// Convert refill rate from per-second to per-millisecond for the Lua script.
	// Using milliseconds gives sub-second precision for the bucket state.
	refillRatePerMS := policy.RefillRatePerSecond() / 1000.0

	// TTL for the Redis key: 2× the window.
	// Ensures the bucket persists across at least 2 full windows
	// (important for burst tracking) but is cleaned up for truly inactive clients.
	ttlMS := policy.Window.Milliseconds() * 2

	// RunScriptCost = 1 (one request = one token consumed).
	// In future: batch operations could pass cost > 1.
	const cost = 1

	result, err := tokenBucketScript.Run(ctx, l.client.RDB(),
		[]string{key},
		capacity,
		refillRatePerMS,
		nowMS,
		cost,
		ttlMS,
	).Slice()
	if err != nil {
		span.RecordError(err)
		return nil, fmt.Errorf("ratelimit: executing token bucket script: %w", err)
	}

	// Parse script return values: [allowed, remaining, reset_ms]
	allowed := result[0].(int64) == 1
	remaining := int(result[1].(int64))
	resetMS := result[2].(int64)
	resetAt := time.UnixMilli(resetMS)

	var retryAfter time.Duration
	if !allowed {
		retryAfter = time.Until(resetAt)
		if retryAfter < 0 {
			retryAfter = 0
		}
	}

	rlResult := &ratelimit.Result{
		Allowed:    allowed,
		Remaining:  remaining,
		Limit:      policy.BucketCapacity(),
		ResetAt:    resetAt,
		RetryAfter: retryAfter,
	}

	span.SetAttributes(
		attribute.Bool("rl.allowed", allowed),
		attribute.Int("rl.remaining", remaining),
	)

	return rlResult, nil
}

// safeKeyPrefix returns the first 30 chars of a key for span attributes.
// Avoids logging full keys (which may contain IP addresses or user IDs)
// in spans that could be exported to third-party trace backends.
func safeKeyPrefix(key string) string {
	if len(key) > 30 {
		return key[:30] + "..."
	}
	return key
}
