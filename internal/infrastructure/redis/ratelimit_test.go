//go:build integration
// +build integration

package redis_test

import (
	"context"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/domain/ratelimit"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
)

// testLimiter creates a rate limiter backed by a real Redis instance (DB 3).
func testLimiter(t *testing.T) *redisinfra.TokenBucketLimiter {
	t.Helper()
	client := testClient(t) // reuse existing testClient helper
	return redisinfra.NewTokenBucketLimiter(client)
}

func TestTokenBucketLimiter_FirstRequest_Allowed(t *testing.T) {
	limiter := testLimiter(t)
	ctx := context.Background()

	policy := ratelimit.Policy{Limit: 10, Window: 60 * time.Second, BurstFactor: 1}
	key := ratelimit.IdentityKey("test", ratelimit.TierFree, ratelimit.ClassWrite, "usr_first")

	result, err := limiter.Check(ctx, key, policy)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if !result.Allowed {
		t.Error("expected first request to be allowed")
	}
	// After consuming 1 token from a full bucket of 10: 9 remaining
	if result.Remaining != 9 {
		t.Errorf("expected 9 remaining, got %d", result.Remaining)
	}
	if result.Limit != 10 {
		t.Errorf("expected limit=10, got %d", result.Limit)
	}
}

func TestTokenBucketLimiter_ExhaustBucket_BlocksRequests(t *testing.T) {
	limiter := testLimiter(t)
	ctx := context.Background()

	policy := ratelimit.Policy{Limit: 5, Window: 60 * time.Second, BurstFactor: 1}
	key := ratelimit.IdentityKey("test", ratelimit.TierFree, ratelimit.ClassWrite, "usr_exhaust")

	// Consume all 5 tokens
	for i := 0; i < 5; i++ {
		result, err := limiter.Check(ctx, key, policy)
		if err != nil {
			t.Fatalf("Check %d failed: %v", i, err)
		}
		if !result.Allowed {
			t.Errorf("request %d should be allowed (bucket not empty yet)", i)
		}
	}

	// 6th request must be blocked
	result, err := limiter.Check(ctx, key, policy)
	if err != nil {
		t.Fatalf("6th Check failed: %v", err)
	}
	if result.Allowed {
		t.Error("expected 6th request to be blocked (bucket empty)")
	}
	if result.Remaining != 0 {
		t.Errorf("expected remaining=0 when blocked, got %d", result.Remaining)
	}
}

func TestTokenBucketLimiter_BurstFactor_AllowsBurst(t *testing.T) {
	limiter := testLimiter(t)
	ctx := context.Background()

	// BurstFactor=2 means capacity=20 even though base limit=10
	policy := ratelimit.Policy{Limit: 10, Window: 60 * time.Second, BurstFactor: 2}
	key := ratelimit.IdentityKey("test", ratelimit.TierFree, ratelimit.ClassRedirect, "usr_burst")

	// Should be able to consume 20 tokens (2× limit)
	for i := 0; i < 20; i++ {
		result, err := limiter.Check(ctx, key, policy)
		if err != nil {
			t.Fatalf("Check %d failed: %v", i, err)
		}
		if !result.Allowed {
			t.Errorf("request %d should be allowed with burst factor 2 (capacity=20)", i)
		}
	}

	// 21st must be blocked
	result, _ := limiter.Check(ctx, key, policy)
	if result.Allowed {
		t.Error("expected 21st request to be blocked (burst capacity exhausted)")
	}
}

func TestTokenBucketLimiter_DifferentKeys_Independent(t *testing.T) {
	limiter := testLimiter(t)
	ctx := context.Background()

	policy := ratelimit.Policy{Limit: 2, Window: 60 * time.Second, BurstFactor: 1}
	keyA := ratelimit.IdentityKey("test", ratelimit.TierFree, ratelimit.ClassWrite, "usr_A")
	keyB := ratelimit.IdentityKey("test", ratelimit.TierFree, ratelimit.ClassWrite, "usr_B")

	// Exhaust user A's bucket
	limiter.Check(ctx, keyA, policy)
	limiter.Check(ctx, keyA, policy)
	resultA, _ := limiter.Check(ctx, keyA, policy)
	if resultA.Allowed {
		t.Error("user A should be rate limited")
	}

	// User B's bucket should be completely independent and full
	resultB, err := limiter.Check(ctx, keyB, policy)
	if err != nil {
		t.Fatalf("user B check failed: %v", err)
	}
	if !resultB.Allowed {
		t.Error("user B should NOT be rate limited (different key)")
	}
}

func TestTokenBucketLimiter_RetryAfter_IsPositive(t *testing.T) {
	limiter := testLimiter(t)
	ctx := context.Background()

	policy := ratelimit.Policy{Limit: 1, Window: 60 * time.Second, BurstFactor: 1}
	key := ratelimit.IdentityKey("test", ratelimit.TierUnauthenticated, ratelimit.ClassWrite, "usr_retry")

	limiter.Check(ctx, key, policy) // consume the one token

	result, err := limiter.Check(ctx, key, policy)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.Allowed {
		t.Fatal("expected blocked")
	}
	if result.RetryAfter <= 0 {
		t.Errorf("expected positive RetryAfter, got %v", result.RetryAfter)
	}
	// RetryAfter should be close to the window duration
	if result.RetryAfter > 65*time.Second {
		t.Errorf("RetryAfter too large: %v (window is 60s)", result.RetryAfter)
	}
}

func TestTokenBucketLimiter_ResetAt_IsFuture(t *testing.T) {
	limiter := testLimiter(t)
	ctx := context.Background()

	policy := ratelimit.Policy{Limit: 10, Window: 60 * time.Second, BurstFactor: 1}
	key := ratelimit.IdentityKey("test", ratelimit.TierFree, ratelimit.ClassRead, "usr_reset")

	result, err := limiter.Check(ctx, key, policy)
	if err != nil {
		t.Fatalf("Check failed: %v", err)
	}
	if result.ResetAt.Before(time.Now()) {
		t.Errorf("expected ResetAt to be in the future, got %v", result.ResetAt)
	}
}
