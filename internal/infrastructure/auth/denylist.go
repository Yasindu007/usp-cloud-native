// Package auth provides the infrastructure adapters for authentication.
// This file implements the JWT token deny list using Redis.
//
// Deny list design:
//
//	When a token is revoked (user logout, admin action, security incident),
//	its JTI (JWT ID) is added to Redis with a TTL equal to the token's
//	remaining lifetime. After the token naturally expires, the deny list
//	entry also expires — no manual cleanup needed.
//
//	Key format:  auth:denylist:jti:{jti}
//	Value:       "1" (existence is what matters, not the value)
//	TTL:         seconds remaining until token expiry
//
// Why Redis for the deny list?
//
//	Every authenticated request performs a deny list check before being
//	served. This must be sub-millisecond. Redis GET on an existing key
//	takes ~0.3ms — acceptable overhead on the auth hot path.
//	An in-memory map would not survive pod restarts and cannot be shared
//	across horizontally scaled pods.
//
// Deny list and availability:
//
//	If Redis is unavailable, the deny list check fails open (allows the
//	request) rather than failing closed (rejects all requests).
//	This is a deliberate reliability-over-security trade-off:
//	- Failing closed would cause a complete API outage whenever Redis blips
//	- The attack window (revoked token still works during Redis outage) is
//	  bounded by the token TTL (max 1 hour) and the Redis outage duration
//	- This trade-off is documented in the security runbook
//	In a higher-security environment, flip this to fail closed.
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName        = "github.com/urlshortener/platform/internal/infrastructure/auth"
	denyListKeyPrefix = "auth:denylist:jti:"
)

// DenyList manages revoked JWT token IDs in Redis.
type DenyList struct {
	rdb *redis.Client
}

// NewDenyList creates a DenyList backed by the given Redis client.
func NewDenyList(rdb *redis.Client) *DenyList {
	return &DenyList{rdb: rdb}
}

// IsRevoked returns true if the given token ID (JTI) has been revoked.
//
// Return semantics:
//
//	(true,  nil)  — token IS in the deny list (revoked) → reject request
//	(false, nil)  — token is NOT in the deny list → proceed
//	(false, err)  — Redis unavailable → fail open (proceed with warning)
//
// Fail-open rationale: see package doc above.
func (d *DenyList) IsRevoked(ctx context.Context, jti string) (bool, error) {
	_, span := otel.Tracer(tracerName).Start(ctx, "DenyList.IsRevoked",
		trace.WithAttributes(
			attribute.String("auth.jti_prefix", jti[:min(8, len(jti))]),
		),
	)
	defer span.End()

	key := denyListKeyPrefix + jti

	// EXISTS returns 1 if the key exists, 0 if it does not.
	count, err := d.rdb.Exists(ctx, key).Result()
	if err != nil {
		span.RecordError(err)
		// Fail open: Redis error → allow request (log at call site)
		return false, fmt.Errorf("denylist: checking JTI %q: %w", jti, err)
	}

	revoked := count > 0
	span.SetAttributes(attribute.Bool("auth.revoked", revoked))
	return revoked, nil
}

// Revoke adds a token JTI to the deny list with a TTL equal to the
// token's remaining lifetime. After the TTL expires, Redis automatically
// removes the key — no manual cleanup required.
//
// expiresAt is the token's "exp" claim. If the token has already expired,
// Revoke is a no-op (the token can't be used anyway).
func (d *DenyList) Revoke(ctx context.Context, jti string, expiresAt time.Time) error {
	_, span := otel.Tracer(tracerName).Start(ctx, "DenyList.Revoke")
	defer span.End()

	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		// Token already expired — no need to store in deny list.
		return nil
	}

	key := denyListKeyPrefix + jti

	// SET with EX — atomic key + TTL in one command.
	// Using SET instead of SETEX because SET supports NX/XX options for future use.
	if err := d.rdb.Set(ctx, key, "1", remaining).Err(); err != nil {
		span.RecordError(err)
		return fmt.Errorf("denylist: revoking JTI %q: %w", jti, err)
	}

	return nil
}

// RevokeAll adds multiple JTIs to the deny list in a single Redis pipeline.
// Used when all tokens for a user must be revoked (e.g., password change,
// security incident). More efficient than calling Revoke in a loop.
func (d *DenyList) RevokeAll(ctx context.Context, tokens []RevokeRequest) error {
	if len(tokens) == 0 {
		return nil
	}

	_, span := otel.Tracer(tracerName).Start(ctx, "DenyList.RevokeAll",
		trace.WithAttributes(
			attribute.Int("auth.token_count", len(tokens)),
		),
	)
	defer span.End()

	// Pipeline batches multiple commands into a single round-trip.
	// For N tokens: 1 network RTT instead of N.
	pipe := d.rdb.Pipeline()
	for _, t := range tokens {
		remaining := time.Until(t.ExpiresAt)
		if remaining <= 0 {
			continue
		}
		pipe.Set(ctx, denyListKeyPrefix+t.JTI, "1", remaining)
	}

	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		span.RecordError(err)
		return fmt.Errorf("denylist: pipeline revoke failed: %w", err)
	}

	return nil
}

// RevokeRequest is the input for RevokeAll.
type RevokeRequest struct {
	JTI       string
	ExpiresAt time.Time
}

// min returns the smaller of two ints.
// Avoids importing "math" for a simple comparison.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
