package keyutil_test

import (
	"strings"
	"testing"

	"github.com/urlshortener/platform/pkg/keyutil"
)

// ── GenerateRaw ───────────────────────────────────────────────────────────────

func TestGenerateRaw_HasCorrectPrefix(t *testing.T) {
	key, err := keyutil.GenerateRaw("01HXYZ")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(key, "urlsk_") {
		t.Errorf("expected key to start with urlsk_, got %q", key[:min(20, len(key))])
	}
}

func TestGenerateRaw_IsUnique(t *testing.T) {
	seen := make(map[string]bool, 100)
	for i := 0; i < 100; i++ {
		key, err := keyutil.GenerateRaw("ws_test")
		if err != nil {
			t.Fatalf("iteration %d: unexpected error: %v", i, err)
		}
		if seen[key] {
			t.Errorf("collision at iteration %d: %q", i, key)
		}
		seen[key] = true
	}
}

func TestGenerateRaw_WorkspacePrefixEmbedded(t *testing.T) {
	wsID := "01HTEST12"
	key, err := keyutil.GenerateRaw(wsID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// After "urlsk_", the first 8 chars should match the workspace ID prefix
	if len(key) < 14 {
		t.Fatalf("key too short: %d chars", len(key))
	}
	embedded := key[6:14] // after "urlsk_", first 8 chars
	if embedded != wsID[:8] {
		t.Errorf("expected workspace prefix %q embedded in key, got %q", wsID[:8], embedded)
	}
}

func TestGenerateRaw_MinimumLength(t *testing.T) {
	key, err := keyutil.GenerateRaw("ws")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// urlsk_(6) + wsPrefix(2) + random(64) = at least 72 chars
	if len(key) < 70 {
		t.Errorf("expected key length >= 70, got %d", len(key))
	}
}

// ── Hash ──────────────────────────────────────────────────────────────────────

func TestHash_ProducesBcryptHash(t *testing.T) {
	key := "urlsk_test1234abcdef"
	hash, err := keyutil.Hash(key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// bcrypt hashes always start with "$2a$" or "$2b$"
	if !strings.HasPrefix(hash, "$2") {
		t.Errorf("expected bcrypt hash prefix $2, got %q", hash[:4])
	}
}

func TestHash_DifferentHashesForSameKey(t *testing.T) {
	// bcrypt generates a new random salt on every call —
	// two hashes of the same key must never be equal.
	key := "urlsk_test1234abcdef"
	hash1, _ := keyutil.Hash(key)
	hash2, _ := keyutil.Hash(key)

	if hash1 == hash2 {
		t.Error("expected different hashes for the same key (bcrypt salting)")
	}
}

// ── Verify ────────────────────────────────────────────────────────────────────

func TestVerify_CorrectKey_ReturnsTrue(t *testing.T) {
	key := "urlsk_correctkey123456789abcdef"
	hash, err := keyutil.Hash(key)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if !keyutil.Verify(key, hash) {
		t.Error("expected Verify=true for correct key")
	}
}

func TestVerify_WrongKey_ReturnsFalse(t *testing.T) {
	key := "urlsk_correctkey123456789abcdef"
	hash, _ := keyutil.Hash(key)

	if keyutil.Verify("urlsk_wrongkey_x", hash) {
		t.Error("expected Verify=false for wrong key")
	}
}

func TestVerify_EmptyKey_ReturnsFalse(t *testing.T) {
	key := "urlsk_somekey"
	hash, _ := keyutil.Hash(key)

	if keyutil.Verify("", hash) {
		t.Error("expected Verify=false for empty key")
	}
}

func TestVerify_EmptyHash_ReturnsFalse(t *testing.T) {
	if keyutil.Verify("urlsk_somekey", "") {
		t.Error("expected Verify=false for empty hash")
	}
}

func TestVerify_TamperedHash_ReturnsFalse(t *testing.T) {
	key := "urlsk_somekey12345678"
	hash, _ := keyutil.Hash(key)
	replacement := "A"
	if hash[len(hash)-1] == 'A' {
		replacement = "B"
	}
	tampered := hash[:len(hash)-1] + replacement

	if keyutil.Verify(key, tampered) {
		t.Error("expected Verify=false for tampered hash")
	}
}

// ── Round-trip ────────────────────────────────────────────────────────────────

func TestGenerateHashVerify_RoundTrip(t *testing.T) {
	// Full pipeline test: generate → hash → verify
	rawKey, err := keyutil.GenerateRaw("ws_test123")
	if err != nil {
		t.Fatalf("GenerateRaw failed: %v", err)
	}

	hash, err := keyutil.Hash(rawKey)
	if err != nil {
		t.Fatalf("Hash failed: %v", err)
	}

	if !keyutil.Verify(rawKey, hash) {
		t.Error("round-trip failed: generated key did not verify against its hash")
	}
}

// ── Benchmarks ────────────────────────────────────────────────────────────────
// Run with: go test -bench=. -benchmem ./pkg/keyutil/
// Expected: ~250ms per Hash op (bcrypt cost=12)

func BenchmarkHash(b *testing.B) {
	key := "urlsk_bench1234567890abcdefghijklmnopqrstuvwxyz0123456789abcdef"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = keyutil.Hash(key)
	}
}

func BenchmarkVerify(b *testing.B) {
	key := "urlsk_bench1234567890abcdefghijklmnopqrstuvwxyz0123456789abcdef"
	hash, _ := keyutil.Hash(key)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		keyutil.Verify(key, hash)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
