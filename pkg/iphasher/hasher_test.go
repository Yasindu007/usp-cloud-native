package iphasher_test

import (
	"testing"
	"time"

	"github.com/urlshortener/platform/pkg/iphasher"
)

func TestHasher_Hash_SameIPSameDay_ProducesSameHash(t *testing.T) {
	h := iphasher.New("test-secret-key")
	ip := "203.0.113.1"

	// Same IP, called twice on the same day → same hash
	hash1 := h.Hash(ip)
	hash2 := h.Hash(ip)

	if hash1 != hash2 {
		t.Error("same IP on same day must produce the same hash")
	}
}

func TestHasher_Hash_DifferentIPs_DifferentHashes(t *testing.T) {
	h := iphasher.New("test-secret-key")

	hash1 := h.Hash("203.0.113.1")
	hash2 := h.Hash("203.0.113.2")

	if hash1 == hash2 {
		t.Error("different IPs must produce different hashes")
	}
}

func TestHasher_Hash_DifferentKeys_DifferentHashes(t *testing.T) {
	// Two hashers with different secret keys must produce different hashes
	// for the same IP — prevents one key compromise from revealing all hashes.
	h1 := iphasher.New("secret-key-A")
	h2 := iphasher.New("secret-key-B")
	ip := "203.0.113.1"

	hash1 := h1.Hash(ip)
	hash2 := h2.Hash(ip)

	if hash1 == hash2 {
		t.Error("different secret keys must produce different hashes for the same IP")
	}
}

func TestHasher_Hash_EmptyIP_ReturnsEmpty(t *testing.T) {
	h := iphasher.New("test-secret")
	if got := h.Hash(""); got != "" {
		t.Errorf("expected empty hash for empty IP, got %q", got)
	}
}

func TestHasher_Hash_ProducesSHA256HexLength(t *testing.T) {
	h := iphasher.New("test-secret")
	hash := h.Hash("203.0.113.1")

	// SHA-256 = 32 bytes = 64 hex characters
	if len(hash) != 64 {
		t.Errorf("expected 64-char hex hash, got %d chars: %q", len(hash), hash)
	}
}

func TestHasher_HashWithDate_DifferentDates_DifferentHashes(t *testing.T) {
	h := iphasher.New("test-secret-key")
	ip := "203.0.113.1"

	day1 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

	hash1 := h.HashWithDate(ip, day1)
	hash2 := h.HashWithDate(ip, day2)

	if hash1 == hash2 {
		t.Error("same IP on different days must produce different hashes (daily salt rotation)")
	}
}

func TestHasher_HashWithDate_SameDay_SameHash(t *testing.T) {
	h := iphasher.New("test-secret-key")
	ip := "203.0.113.1"
	day := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)

	// Morning and evening of the same day → same hash
	morning := h.HashWithDate(ip, day)
	evening := h.HashWithDate(ip, day.Add(18*time.Hour))

	if morning != evening {
		t.Error("same IP on same day (different times) must produce the same hash")
	}
}

func TestHasher_Hash_IPv6_Supported(t *testing.T) {
	h := iphasher.New("test-secret")
	ipv6 := "2001:db8::1"
	hash := h.Hash(ipv6)

	if hash == "" {
		t.Error("expected non-empty hash for IPv6 address")
	}
	if len(hash) != 64 {
		t.Errorf("expected 64-char hash for IPv6, got %d", len(hash))
	}
}

func BenchmarkHasher_Hash(b *testing.B) {
	h := iphasher.New("benchmark-secret-key")
	ip := "203.0.113.42"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.Hash(ip)
	}
}
