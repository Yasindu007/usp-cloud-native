//go:build integration
// +build integration

package auth_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	infraauth "github.com/urlshortener/platform/internal/infrastructure/auth"
)

// testDenyList creates a DenyList backed by a real Redis instance.
// Uses DB 2 for tests to isolate from dev (DB 0) and cache tests (DB 1).
func testDenyList(t *testing.T) *infraauth.DenyList {
	t.Helper()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	password := os.Getenv("REDIS_PASSWORD")
	if password == "" {
		password = "secret"
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       2, // dedicated test DB
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping failed: %v\nrun: make infra-up", err)
	}

	t.Cleanup(func() {
		_ = rdb.FlushDB(context.Background())
		_ = rdb.Close()
	})

	return infraauth.NewDenyList(rdb)
}

func TestDenyList_IsRevoked_FreshToken_NotRevoked(t *testing.T) {
	dl := testDenyList(t)
	ctx := context.Background()

	revoked, err := dl.IsRevoked(ctx, "fresh-jti-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if revoked {
		t.Error("expected IsRevoked=false for a token not in the deny list")
	}
}

func TestDenyList_Revoke_And_IsRevoked(t *testing.T) {
	dl := testDenyList(t)
	ctx := context.Background()

	jti := "test-jti-revoke-001"
	expiresAt := time.Now().Add(1 * time.Hour)

	if err := dl.Revoke(ctx, jti, expiresAt); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	revoked, err := dl.IsRevoked(ctx, jti)
	if err != nil {
		t.Fatalf("IsRevoked failed: %v", err)
	}
	if !revoked {
		t.Error("expected IsRevoked=true after Revoke")
	}
}

func TestDenyList_Revoke_ExpiredToken_NoOp(t *testing.T) {
	dl := testDenyList(t)
	ctx := context.Background()

	jti := "expired-jti-001"
	// Token already expired
	expiresAt := time.Now().Add(-1 * time.Hour)

	// Should not error even though token is expired
	if err := dl.Revoke(ctx, jti, expiresAt); err != nil {
		t.Fatalf("Revoke of expired token should not error: %v", err)
	}

	// Should not be in deny list (no-op)
	revoked, err := dl.IsRevoked(ctx, jti)
	if err != nil {
		t.Fatalf("IsRevoked failed: %v", err)
	}
	if revoked {
		t.Error("expected IsRevoked=false for an already-expired token")
	}
}

func TestDenyList_Revoke_TTLMatchesTokenLifetime(t *testing.T) {
	dl := testDenyList(t)
	ctx := context.Background()

	jti := "ttl-test-jti-001"
	expiresAt := time.Now().Add(2 * time.Second)

	if err := dl.Revoke(ctx, jti, expiresAt); err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}

	// Immediately after revocation: should be revoked
	revoked, _ := dl.IsRevoked(ctx, jti)
	if !revoked {
		t.Fatal("expected revoked immediately after Revoke")
	}

	// Wait for TTL to expire
	time.Sleep(3 * time.Second)

	// After TTL: should be gone from deny list
	revoked, err := dl.IsRevoked(ctx, jti)
	if err != nil {
		t.Fatalf("IsRevoked after TTL failed: %v", err)
	}
	if revoked {
		t.Error("expected IsRevoked=false after TTL expiry")
	}
}

func TestDenyList_RevokeAll_MultpleTokens(t *testing.T) {
	dl := testDenyList(t)
	ctx := context.Background()

	tokens := []infraauth.RevokeRequest{
		{JTI: "bulk-jti-001", ExpiresAt: time.Now().Add(1 * time.Hour)},
		{JTI: "bulk-jti-002", ExpiresAt: time.Now().Add(30 * time.Minute)},
		{JTI: "bulk-jti-003", ExpiresAt: time.Now().Add(-1 * time.Hour)}, // already expired
	}

	if err := dl.RevokeAll(ctx, tokens); err != nil {
		t.Fatalf("RevokeAll failed: %v", err)
	}

	// JTI 001 and 002 should be revoked
	for _, jti := range []string{"bulk-jti-001", "bulk-jti-002"} {
		revoked, err := dl.IsRevoked(ctx, jti)
		if err != nil {
			t.Errorf("IsRevoked(%s) failed: %v", jti, err)
		}
		if !revoked {
			t.Errorf("expected IsRevoked=true for %s", jti)
		}
	}

	// JTI 003 should NOT be revoked (already expired, no-op)
	revoked, _ := dl.IsRevoked(ctx, "bulk-jti-003")
	if revoked {
		t.Error("expected IsRevoked=false for already-expired token in RevokeAll")
	}
}
