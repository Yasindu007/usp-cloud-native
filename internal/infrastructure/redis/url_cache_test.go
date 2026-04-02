//go:build integration
// +build integration

// Integration tests require a running Redis instance.
// Run with: go test -v -tags=integration ./internal/infrastructure/redis/...
//
// Locally: make infra-up before running.
// CI: runs in a separate job with a Redis service container (Phase 4).

package redis_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/config"
	"github.com/urlshortener/platform/internal/domain/url"
	redisinfra "github.com/urlshortener/platform/internal/infrastructure/redis"
)

// testClient creates a real Redis client for integration tests.
func testClient(t *testing.T) *redisinfra.Client {
	t.Helper()

	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}
	password := os.Getenv("REDIS_PASSWORD")
	if password == "" {
		password = "secret"
	}

	cfg := &config.Config{
		RedisAddr:          addr,
		RedisPassword:      password,
		RedisDB:            1, // Use DB 1 for tests to isolate from dev data
		RedisPoolSize:      5,
		RedisMinIdleConns:  1,
		RedisDialTimeoutS:  5,
		RedisReadTimeoutS:  3,
		RedisWriteTimeoutS: 3,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	client, err := redisinfra.New(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to connect to test redis: %v\n"+
			"Is redis running? Run: make infra-up", err)
	}

	t.Cleanup(func() {
		// Flush test DB after each test run to avoid state leakage.
		// DB 1 is used exclusively for tests — safe to flush.
		_ = client.RDB().FlushDB(context.Background())
		_ = client.Close()
	})

	return client
}

// testURLCache creates a URLCache backed by the test client.
func testURLCache(t *testing.T) *redisinfra.URLCache {
	t.Helper()
	return redisinfra.NewURLCache(testClient(t))
}

// newTestURL builds a minimal URL entity for testing.
func newTestURL(shortCode string) *url.URL {
	return &url.URL{
		ID:          "01HTEST" + shortCode,
		WorkspaceID: "ws_test",
		ShortCode:   shortCode,
		OriginalURL: "https://example.com/" + shortCode,
		Title:       "Test URL",
		Status:      url.StatusActive,
		CreatedBy:   "user_test",
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
		UpdatedAt:   time.Now().UTC().Truncate(time.Millisecond),
		ClickCount:  0,
	}
}

// ── Get / Set ─────────────────────────────────────────────────────────────

func TestURLCache_Set_And_Get_Hit(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	u := newTestURL("abc1234")

	if err := cache.Set(ctx, u, 60); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, err := cache.Get(ctx, u.ShortCode)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected cache hit, got nil (cache miss)")
	}

	if got.ID != u.ID {
		t.Errorf("ID mismatch: want %q, got %q", u.ID, got.ID)
	}
	if got.OriginalURL != u.OriginalURL {
		t.Errorf("OriginalURL mismatch: want %q, got %q", u.OriginalURL, got.OriginalURL)
	}
	if got.ShortCode != u.ShortCode {
		t.Errorf("ShortCode mismatch: want %q, got %q", u.ShortCode, got.ShortCode)
	}
	if got.Status != url.StatusActive {
		t.Errorf("Status mismatch: want active, got %q", got.Status)
	}
}

func TestURLCache_Get_Miss(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	// Requesting a key we never set must return (nil, nil) — a clean miss.
	got, err := cache.Get(ctx, "doesnotexist")
	if err != nil {
		t.Fatalf("expected nil error on cache miss, got: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil on cache miss, got: %+v", got)
	}
}

func TestURLCache_Get_ReturnsNilAfterTTLExpiry(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	u := newTestURL("expire1")

	// Set with 1-second TTL.
	if err := cache.Set(ctx, u, 1); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Immediately readable.
	got, _ := cache.Get(ctx, u.ShortCode)
	if got == nil {
		t.Fatal("expected hit immediately after Set, got miss")
	}

	// Wait for TTL to expire.
	time.Sleep(1500 * time.Millisecond)

	// Now must be a miss.
	expired, err := cache.Get(ctx, u.ShortCode)
	if err != nil {
		t.Fatalf("Get after expiry returned error: %v", err)
	}
	if expired != nil {
		t.Error("expected cache miss after TTL expiry, got a hit")
	}
}

func TestURLCache_Set_PreservesExpiresAt(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	expiry := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Millisecond)
	u := newTestURL("exp2")
	u.ExpiresAt = &expiry

	if err := cache.Set(ctx, u, 3600); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, err := cache.Get(ctx, u.ShortCode)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected cache hit")
	}
	if got.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be preserved, got nil")
	}
	if !got.ExpiresAt.Equal(expiry) {
		t.Errorf("ExpiresAt mismatch: want %v, got %v", expiry, *got.ExpiresAt)
	}
}

// ── Negative Cache ────────────────────────────────────────────────────────

func TestURLCache_SetNotFound_And_IsNotFound(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	shortCode := "ghost99"

	// Before setting: IsNotFound must return false.
	found, err := cache.IsNotFound(ctx, shortCode)
	if err != nil {
		t.Fatalf("IsNotFound pre-set returned error: %v", err)
	}
	if found {
		t.Error("expected IsNotFound=false before SetNotFound")
	}

	// Set negative cache.
	if err := cache.SetNotFound(ctx, shortCode, 60); err != nil {
		t.Fatalf("SetNotFound failed: %v", err)
	}

	// Now IsNotFound must return true.
	found, err = cache.IsNotFound(ctx, shortCode)
	if err != nil {
		t.Fatalf("IsNotFound post-set returned error: %v", err)
	}
	if !found {
		t.Error("expected IsNotFound=true after SetNotFound")
	}

	// Positive Get must still return nil (negative cache is a separate key).
	got, err := cache.Get(ctx, shortCode)
	if err != nil {
		t.Fatalf("Get after SetNotFound returned error: %v", err)
	}
	if got != nil {
		t.Error("expected Get=nil when only negative cache is set")
	}
}

func TestURLCache_SetNotFound_ExpiresAfterTTL(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	shortCode := "tempghost"

	if err := cache.SetNotFound(ctx, shortCode, 1); err != nil {
		t.Fatalf("SetNotFound failed: %v", err)
	}

	found, _ := cache.IsNotFound(ctx, shortCode)
	if !found {
		t.Fatal("expected IsNotFound=true immediately after SetNotFound")
	}

	time.Sleep(1500 * time.Millisecond)

	found, err := cache.IsNotFound(ctx, shortCode)
	if err != nil {
		t.Fatalf("IsNotFound after TTL returned error: %v", err)
	}
	if found {
		t.Error("expected IsNotFound=false after TTL expiry")
	}
}

// ── Delete ────────────────────────────────────────────────────────────────

func TestURLCache_Delete_RemovesPositiveEntry(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	u := newTestURL("del001")
	_ = cache.Set(ctx, u, 3600)

	if err := cache.Delete(ctx, u.ShortCode); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	got, err := cache.Get(ctx, u.ShortCode)
	if err != nil {
		t.Fatalf("Get after Delete returned error: %v", err)
	}
	if got != nil {
		t.Error("expected cache miss after Delete, got a hit")
	}
}

func TestURLCache_Delete_RemovesNegativeEntry(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	shortCode := "del002"
	_ = cache.SetNotFound(ctx, shortCode, 3600)

	if err := cache.Delete(ctx, shortCode); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	found, err := cache.IsNotFound(ctx, shortCode)
	if err != nil {
		t.Fatalf("IsNotFound after Delete returned error: %v", err)
	}
	if found {
		t.Error("expected IsNotFound=false after Delete")
	}
}

func TestURLCache_Delete_IdempotentOnMissingKey(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	// Deleting a key that doesn't exist must not return an error.
	if err := cache.Delete(ctx, "neverexisted"); err != nil {
		t.Errorf("Delete on missing key returned error: %v", err)
	}
}

// ── TTL ────────────────────────────────────────────────────────────────────

func TestURLCache_TTL_ReturnsApproximateTTL(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	u := newTestURL("ttl001")
	if err := cache.Set(ctx, u, 3600); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	ttl, err := cache.TTL(ctx, u.ShortCode)
	if err != nil {
		t.Fatalf("TTL failed: %v", err)
	}

	// TTL should be between 3598s and 3601s (allowing for slight clock drift).
	if ttl < 3598*time.Second || ttl > 3601*time.Second {
		t.Errorf("expected TTL ~3600s, got %v", ttl)
	}
}

// ── Ping ───────────────────────────────────────────────────────────────────

func TestClient_Ping(t *testing.T) {
	client := testClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := client.Ping(ctx); err != nil {
		t.Errorf("Ping failed: %v", err)
	}
}

// ── Overwrite behavior ─────────────────────────────────────────────────────

func TestURLCache_Set_OverwritesExistingEntry(t *testing.T) {
	cache := testURLCache(t)
	ctx := context.Background()

	u := newTestURL("over01")
	_ = cache.Set(ctx, u, 3600)

	// Update the URL and re-cache it.
	u.OriginalURL = "https://updated.example.com/over01"
	if err := cache.Set(ctx, u, 3600); err != nil {
		t.Fatalf("Set overwrite failed: %v", err)
	}

	got, _ := cache.Get(ctx, u.ShortCode)
	if got == nil {
		t.Fatal("expected hit after overwrite")
	}
	if got.OriginalURL != "https://updated.example.com/over01" {
		t.Errorf("expected updated URL, got %q", got.OriginalURL)
	}
}
