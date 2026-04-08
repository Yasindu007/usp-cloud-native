package ratelimit_test

import (
	"testing"
	"time"

	"github.com/urlshortener/platform/internal/domain/ratelimit"
)

func TestGetPolicy_KnownTierAndClass(t *testing.T) {
	cases := []struct {
		tier       ratelimit.Tier
		class      ratelimit.EndpointClass
		wantLimit  int
		wantWindow time.Duration
	}{
		{ratelimit.TierFree, ratelimit.ClassWrite, 100, 60 * time.Second},
		{ratelimit.TierFree, ratelimit.ClassRedirect, 1000, 60 * time.Second},
		{ratelimit.TierPro, ratelimit.ClassWrite, 1000, 60 * time.Second},
		{ratelimit.TierUnauthenticated, ratelimit.ClassWrite, 10, 60 * time.Second},
		{ratelimit.TierEnterprise, ratelimit.ClassWrite, 10000, 60 * time.Second},
	}

	for _, tc := range cases {
		t.Run(string(tc.tier)+"/"+string(tc.class), func(t *testing.T) {
			p := ratelimit.GetPolicy(tc.tier, tc.class)
			if p.Limit != tc.wantLimit {
				t.Errorf("Limit: want %d, got %d", tc.wantLimit, p.Limit)
			}
			if p.Window != tc.wantWindow {
				t.Errorf("Window: want %v, got %v", tc.wantWindow, p.Window)
			}
		})
	}
}

func TestGetPolicy_UnknownTier_ReturnsDefault(t *testing.T) {
	p := ratelimit.GetPolicy("unknown_tier", ratelimit.ClassWrite)
	// Must return a non-zero policy (safe fallback)
	if p.Limit == 0 {
		t.Error("expected non-zero limit for unknown tier fallback")
	}
}

func TestPolicy_BucketCapacity(t *testing.T) {
	p := ratelimit.Policy{Limit: 100, Window: 60 * time.Second, BurstFactor: 1}
	if p.BucketCapacity() != 100 {
		t.Errorf("BurstFactor=1: expected capacity=100, got %d", p.BucketCapacity())
	}

	p2 := ratelimit.Policy{Limit: 100, Window: 60 * time.Second, BurstFactor: 2}
	if p2.BucketCapacity() != 200 {
		t.Errorf("BurstFactor=2: expected capacity=200, got %d", p2.BucketCapacity())
	}
}

func TestPolicy_RefillRatePerSecond(t *testing.T) {
	p := ratelimit.Policy{Limit: 60, Window: 60 * time.Second}
	// 60 tokens / 60 seconds = 1 token/second
	if p.RefillRatePerSecond() != 1.0 {
		t.Errorf("expected 1.0 tokens/s, got %v", p.RefillRatePerSecond())
	}

	p2 := ratelimit.Policy{Limit: 1000, Window: 60 * time.Second}
	want := 1000.0 / 60.0
	if got := p2.RefillRatePerSecond(); got < want-0.01 || got > want+0.01 {
		t.Errorf("expected ~%.4f tokens/s, got %.4f", want, got)
	}
}

func TestResult_Headers_Allowed(t *testing.T) {
	r := &ratelimit.Result{
		Allowed:   true,
		Remaining: 42,
		Limit:     100,
		ResetAt:   time.Unix(1700000000, 0),
	}
	h := r.Headers()

	if h["RateLimit-Limit"] != "100" {
		t.Errorf("expected RateLimit-Limit=100, got %q", h["RateLimit-Limit"])
	}
	if h["RateLimit-Remaining"] != "42" {
		t.Errorf("expected RateLimit-Remaining=42, got %q", h["RateLimit-Remaining"])
	}
	if h["RateLimit-Reset"] != "1700000000" {
		t.Errorf("expected RateLimit-Reset=1700000000, got %q", h["RateLimit-Reset"])
	}
	// Retry-After must NOT be present when allowed
	if _, ok := h["Retry-After"]; ok {
		t.Error("Retry-After must not be set when Allowed=true")
	}
}

func TestResult_Headers_Denied(t *testing.T) {
	r := &ratelimit.Result{
		Allowed:    false,
		Remaining:  0,
		Limit:      100,
		ResetAt:    time.Now().Add(30 * time.Second),
		RetryAfter: 30 * time.Second,
	}
	h := r.Headers()

	if h["Retry-After"] != "30" {
		t.Errorf("expected Retry-After=30, got %q", h["Retry-After"])
	}
}

func TestIdentityKey_Format(t *testing.T) {
	key := ratelimit.IdentityKey("api", ratelimit.TierFree, ratelimit.ClassWrite, "usr_001")
	expected := "rl:api:free:write:usr_001"
	if key != expected {
		t.Errorf("expected %q, got %q", expected, key)
	}
}

func TestIdentityKey_IPv6_ColonReplaced(t *testing.T) {
	// IPv6 addresses contain colons — they must be sanitised to
	// prevent key injection into the Redis key structure.
	key := ratelimit.IdentityKey("redirect", ratelimit.TierUnauthenticated,
		ratelimit.ClassRedirect, "2001:db8::1")
	if key == "rl:redirect:unauthenticated:redirect:2001:db8::1" {
		t.Error("IPv6 colons must be replaced to prevent key injection")
	}
	// Should contain underscores instead of colons in the identity part
	if key != "rl:redirect:unauthenticated:redirect:2001_db8__1" {
		t.Errorf("expected colon→underscore substitution, got %q", key)
	}
}
